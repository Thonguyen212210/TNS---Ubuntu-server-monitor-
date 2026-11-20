package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Azure IMDS + ARM REST API integration (zero external SDKs).
// Uses the VM's System-Assigned Managed Identity to manage the Resource Group's
// Virtual Network, Subnets, NSGs, Route Tables, NICs, Public IPs, and VMs.

type imdsMetadata struct {
	Compute struct {
		Location          string `json:"location"`
		Name              string `json:"name"`
		ResourceGroupName string `json:"resourceGroupName"`
		SubscriptionID    string `json:"subscriptionId"`
		VMSize            string `json:"vmSize"`
		OSType            string `json:"osType"`
		SKU               string `json:"sku"`
	} `json:"compute"`
	Network struct {
		Interface []struct {
			IPv4 struct {
				IPAddress []struct {
					PrivateIPAddress string `json:"privateIpAddress"`
					PublicIPAddress  string `json:"publicIpAddress"`
				} `json:"ipAddress"`
				Subnet []struct {
					Address string `json:"address"`
					Prefix  string `json:"prefix"`
				} `json:"subnet"`
			} `json:"ipv4"`
			MACAddress string `json:"macAddress"`
		} `json:"interface"`
	} `json:"network"`
}

var (
	imdsMu     sync.Mutex
	imdsCache  *imdsMetadata
	imdsAt     time.Time
	imdsClient = &http.Client{Timeout: 2500 * time.Millisecond}

	tokMu    sync.Mutex
	tokVal   string
	tokExp   time.Time
	armHTTP  = &http.Client{Timeout: 25 * time.Second}
	reAzName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,79}$`)

	localNetMu sync.Mutex
	localPubIP string
	localFQDN  string
	localNetAt time.Time
)

func initAzure() {
	go func() {
		if getIMDSCached() != nil {
			_, _ = getLocalAzurePublicNet()
		}
	}()
}

func setLocalAzurePublicNet(ip, fqdn string) {
	localNetMu.Lock()
	localPubIP = ip
	localFQDN = fqdn
	localNetAt = time.Now()
	localNetMu.Unlock()
}

// getLocalAzurePublicNet returns the local Azure VM's Public IP and DNS FQDN (cached 5 min).
func getLocalAzurePublicNet() (pubIP, fqdn string) {
	localNetMu.Lock()
	if !localNetAt.IsZero() && time.Since(localNetAt) < 5*time.Minute {
		ip, f := localPubIP, localFQDN
		localNetMu.Unlock()
		return ip, f
	}
	localNetMu.Unlock()

	subID, rg, _, localVM, ok := azureBaseScope()
	if !ok {
		return "", ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	base := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network", subID, rg)
	bNics, _, err := armDo(ctx, "GET", base+"/networkInterfaces?api-version=2023-11-01", nil)
	if err != nil {
		return "", ""
	}
	var rawNics struct {
		Value []struct {
			Properties struct {
				VirtualMachine   *struct{ ID string `json:"id"` } `json:"virtualMachine"`
				IPConfigurations []struct {
					Properties struct {
						PublicIPAddress *struct{ ID string `json:"id"` } `json:"publicIPAddress"`
					} `json:"properties"`
				} `json:"ipConfigurations"`
			} `json:"properties"`
		} `json:"value"`
	}
	_ = json.Unmarshal(bNics, &rawNics)
	var pipID string
	for _, n := range rawNics.Value {
		if n.Properties.VirtualMachine != nil && strings.EqualFold(armIDTail(n.Properties.VirtualMachine.ID), localVM) {
			if len(n.Properties.IPConfigurations) > 0 && n.Properties.IPConfigurations[0].Properties.PublicIPAddress != nil {
				pipID = n.Properties.IPConfigurations[0].Properties.PublicIPAddress.ID
			}
			break
		}
	}
	if pipID == "" {
		setLocalAzurePublicNet("", "")
		return "", ""
	}
	bPip, _, err := armDo(ctx, "GET", pipID+"?api-version=2023-11-01", nil)
	if err != nil {
		return "", ""
	}
	var pip struct {
		Properties struct {
			IPAddress   string `json:"ipAddress"`
			DNSSettings *struct {
				FQDN string `json:"fqdn"`
			} `json:"dnsSettings"`
		} `json:"properties"`
	}
	if json.Unmarshal(bPip, &pip) == nil {
		pubIP = pip.Properties.IPAddress
		if pip.Properties.DNSSettings != nil {
			fqdn = pip.Properties.DNSSettings.FQDN
		}
	}
	setLocalAzurePublicNet(pubIP, fqdn)
	return pubIP, fqdn
}

func getIMDSCached() *imdsMetadata {
	imdsMu.Lock()
	defer imdsMu.Unlock()
	if imdsCache != nil && time.Since(imdsAt) < 10*time.Minute {
		return imdsCache
	}
	if imdsCache == nil && !imdsAt.IsZero() && time.Since(imdsAt) < 2*time.Minute {
		return nil
	}
	imdsAt = time.Now()
	req, _ := http.NewRequest("GET", "http://169.254.169.254/metadata/instance?api-version=2021-02-01", nil)
	req.Header.Set("Metadata", "true")
	resp, err := imdsClient.Do(req)
	if err != nil {
		return imdsCache
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return imdsCache
	}
	var md imdsMetadata
	if json.NewDecoder(resp.Body).Decode(&md) == nil && md.Compute.SubscriptionID != "" {
		imdsCache = &md
	}
	return imdsCache
}

func getARMToken(ctx context.Context) (string, error) {
	tokMu.Lock()
	defer tokMu.Unlock()
	if tokVal != "" && time.Now().Before(tokExp.Add(-5*time.Minute)) {
		return tokVal, nil
	}
	req, err := http.NewRequestWithContext(ctx, "GET",
		"http://169.254.169.254/metadata/identity/oauth2/token?api-version=2018-02-01&resource=https://management.azure.com/", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Metadata", "true")
	resp, err := imdsClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("unable to connect to Azure IMDS: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		var er struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		_ = json.Unmarshal(b, &er)
		if er.Description != "" {
			return "", fmt.Errorf("Managed Identity: %s", er.Description)
		}
		return "", fmt.Errorf("IMDS token HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresOn   string `json:"expires_on"`
	}
	if err := json.Unmarshal(b, &tr); err != nil || tr.AccessToken == "" {
		return "", fmt.Errorf("failed to decode Azure token")
	}
	expSec, _ := strconv.ParseInt(tr.ExpiresOn, 10, 64)
	tokVal = tr.AccessToken
	if expSec > 0 {
		tokExp = time.Unix(expSec, 0)
	} else {
		tokExp = time.Now().Add(30 * time.Minute)
	}
	return tokVal, nil
}

func armDo(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	tok, err := getARMToken(ctx)
	if err != nil {
		return nil, 0, err
	}
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://management.azure.com"+path, reader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := armHTTP.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode >= 400 {
		var armErr struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(b, &armErr) == nil && armErr.Error.Message != "" {
			return b, resp.StatusCode, fmt.Errorf("%s: %s", armErr.Error.Code, armErr.Error.Message)
		}
		return b, resp.StatusCode, fmt.Errorf("Azure API HTTP %d: %s", resp.StatusCode, firstLine(string(b)))
	}
	return b, resp.StatusCode, nil
}

func azureBaseScope() (subID, rg, loc, vmName string, ok bool) {
	if md := getIMDSCached(); md != nil && md.Compute.SubscriptionID != "" && md.Compute.ResourceGroupName != "" {
		return md.Compute.SubscriptionID, md.Compute.ResourceGroupName, md.Compute.Location, md.Compute.Name, true
	}
	return "", "", "", "", false
}

func armIDTail(id string) string {
	id = strings.TrimRight(id, "/")
	if i := strings.LastIndexByte(id, '/'); i >= 0 {
		return id[i+1:]
	}
	return id
}

func armIDSegmentAfter(id, key string) string {
	parts := strings.Split(id, "/")
	for i := 0; i < len(parts)-1; i++ {
		if strings.EqualFold(parts[i], key) {
			return parts[i+1]
		}
	}
	return ""
}

// ---------- local gateway telemetry ----------

type GatewayInfo struct {
	IPForward       bool           `json:"ipForward"`
	NATRules        []string       `json:"natRules"`
	TinyproxyActive bool           `json:"tinyproxyActive"`
	TailscaleIP     string         `json:"tailscaleIp,omitempty"`
	PrivateIP       string         `json:"privateIp,omitempty"`
	PeerChecks      []PeerNetCheck `json:"peerChecks"`
}

type PeerNetCheck struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IP        string `json:"ip"`
	Reachable bool   `json:"reachable"`
	SSHOpen   bool   `json:"sshOpen"`
	AgentOpen bool   `json:"agentOpen"`
	RTTMs     int64  `json:"rttMs"`
}

func collectGatewayInfo() GatewayInfo {
	gw := GatewayInfo{NATRules: []string{}, PeerChecks: []PeerNetCheck{}}
	gw.PrivateIP, gw.TailscaleIP = localPrivateAndTailscaleIP()
	if b, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward"); err == nil {
		gw.IPForward = strings.TrimSpace(string(b)) == "1"
	}
	if hasCmd("iptables") {
		if out, err := runCmd(5*time.Second, "iptables", "-t", "nat", "-S", "POSTROUTING"); err == nil {
			for _, l := range strings.Split(out, "\n") {
				l = strings.TrimSpace(l)
				if strings.HasPrefix(l, "-A POSTROUTING") {
					gw.NATRules = append(gw.NATRules, l)
				}
			}
		}
	}
	if hasCmd("systemctl") {
		if out, err := runCmd(3*time.Second, "systemctl", "is-active", "tinyproxy"); err == nil {
			gw.TinyproxyActive = strings.TrimSpace(out) == "active"
		}
	}

	peers := getAllPeers()
	checks := make([]PeerNetCheck, len(peers))
	var wg sync.WaitGroup
	for i, p := range peers {
		wg.Add(1)
		go func(idx int, cfg NodeConfig) {
			defer wg.Done()
			ip := cfg.PrivateIP
			c := PeerNetCheck{ID: cfg.ID, Name: cfg.Name, IP: ip}
			if ip == "" {
				checks[idx] = c
				return
			}
			t0 := time.Now()
			if conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip, "22"), 1500*time.Millisecond); err == nil {
				c.SSHOpen = true
				c.Reachable = true
				c.RTTMs = time.Since(t0).Milliseconds()
				_ = conn.Close()
			}
			if conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip, "1611"), 1500*time.Millisecond); err == nil {
				c.AgentOpen = true
				c.Reachable = true
				if c.RTTMs == 0 {
					c.RTTMs = time.Since(t0).Milliseconds()
				}
				_ = conn.Close()
			}
			checks[idx] = c
		}(i, p)
	}
	wg.Wait()
	gw.PeerChecks = checks
	return gw
}

// ---------- GET /api/azure/overview ----------

func handleAzureOverview(w http.ResponseWriter, r *http.Request) {
	subID, rg, loc, localVM, ok := azureBaseScope()
	gw := collectGatewayInfo()
	if !ok {
		writeJSON(w, map[string]any{
			"available": false,
			"note":      "Azure IMDS was not detected on this server (not running on an Azure VM).",
			"gateway":   gw,
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	// Verify token first
	if _, err := getARMToken(ctx); err != nil {
		writeJSON(w, map[string]any{
			"available":      false,
			"subscriptionId": subID,
			"resourceGroup":  rg,
			"location":       loc,
			"localVm":        localVM,
			"authError":      err.Error(),
			"note":           "This VM does not have a Managed Identity or lacks permissions on Resource Group " + rg + ".",
			"gateway":        gw,
		})
		return
	}

	base := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s", subID, rg)
	type armResp struct {
		body []byte
		err  error
	}
	keys := []string{"vnets", "nsgs", "rts", "nics", "pips", "vms", "resources"}
	paths := map[string]string{
		"vnets":     base + "/providers/Microsoft.Network/virtualNetworks?api-version=2023-11-01",
		"nsgs":      base + "/providers/Microsoft.Network/networkSecurityGroups?api-version=2023-11-01",
		"rts":       base + "/providers/Microsoft.Network/routeTables?api-version=2023-11-01",
		"nics":      base + "/providers/Microsoft.Network/networkInterfaces?api-version=2023-11-01",
		"pips":      base + "/providers/Microsoft.Network/publicIPAddresses?api-version=2023-11-01",
		"vms":       base + "/providers/Microsoft.Compute/virtualMachines?api-version=2024-03-01",
		"resources": base + "/resources?api-version=2021-04-01",
	}

	res := make(map[string]armResp, len(keys))
	var rmu sync.Mutex
	var wg sync.WaitGroup
	for _, k := range keys {
		wg.Add(1)
		go func(key, p string) {
			defer wg.Done()
			b, _, err := armDo(ctx, "GET", p, nil)
			rmu.Lock()
			res[key] = armResp{body: b, err: err}
			rmu.Unlock()
		}(k, paths[k])
	}
	wg.Wait()

	var errs []string
	for _, k := range keys {
		if res[k].err != nil {
			errs = append(errs, k+": "+res[k].err.Error())
		}
	}

	// Parse Public IPs
	type pipItem struct {
		Name       string `json:"name"`
		ID         string `json:"id"`
		IP         string `json:"ip"`
		FQDN       string `json:"fqdn,omitempty"`
		DNSLabel   string `json:"dnsLabel,omitempty"`
		Allocation string `json:"allocation"`
		SKU        string `json:"sku"`
		NIC        string `json:"nic"`
	}
	var pips []pipItem
	pipByID := map[string]pipItem{}
	var rawPips struct {
		Value []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			SKU        struct{ Name string `json:"name"` } `json:"sku"`
			Properties struct {
				IPAddress                string `json:"ipAddress"`
				PublicIPAllocationMethod string `json:"publicIPAllocationMethod"`
				DNSSettings              *struct {
					DomainNameLabel string `json:"domainNameLabel"`
					FQDN            string `json:"fqdn"`
				} `json:"dnsSettings"`
				IPConfiguration *struct{ ID string `json:"id"` } `json:"ipConfiguration"`
			} `json:"properties"`
		} `json:"value"`
	}
	_ = json.Unmarshal(res["pips"].body, &rawPips)
	for _, p := range rawPips.Value {
		it := pipItem{
			Name:       p.Name,
			ID:         p.ID,
			IP:         p.Properties.IPAddress,
			Allocation: p.Properties.PublicIPAllocationMethod,
			SKU:        p.SKU.Name,
		}
		if p.Properties.DNSSettings != nil {
			it.FQDN = p.Properties.DNSSettings.FQDN
			it.DNSLabel = p.Properties.DNSSettings.DomainNameLabel
		}
		if p.Properties.IPConfiguration != nil {
			it.NIC = armIDSegmentAfter(p.Properties.IPConfiguration.ID, "networkInterfaces")
		}
		pips = append(pips, it)
		pipByID[strings.ToLower(p.ID)] = it
	}

	// Parse NICs
	type nicItem struct {
		Name               string `json:"name"`
		ID                 string `json:"id"`
		VM                 string `json:"vm"`
		VNet               string `json:"vnet"`
		Subnet             string `json:"subnet"`
		PrivateIP          string `json:"privateIp"`
		PublicIP           string `json:"publicIp"`
		PublicIPName       string `json:"publicIpName"`
		FQDN               string `json:"fqdn,omitempty"`
		NSG                string `json:"nsg"`
		MAC                string `json:"mac"`
		EnableIPForwarding bool   `json:"enableIPForwarding"`
	}
	var nics []nicItem
	nicByID := map[string]nicItem{}
	var rawNics struct {
		Value []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Properties struct {
				EnableIPForwarding   bool   `json:"enableIPForwarding"`
				MacAddress           string `json:"macAddress"`
				VirtualMachine       *struct{ ID string `json:"id"` } `json:"virtualMachine"`
				NetworkSecurityGroup *struct{ ID string `json:"id"` } `json:"networkSecurityGroup"`
				IPConfigurations     []struct {
					Properties struct {
						PrivateIPAddress string `json:"privateIPAddress"`
						Subnet           *struct{ ID string `json:"id"` } `json:"subnet"`
						PublicIPAddress  *struct{ ID string `json:"id"` } `json:"publicIPAddress"`
					} `json:"properties"`
				} `json:"ipConfigurations"`
			} `json:"properties"`
		} `json:"value"`
	}
	_ = json.Unmarshal(res["nics"].body, &rawNics)
	for _, n := range rawNics.Value {
		it := nicItem{
			Name:               n.Name,
			ID:                 n.ID,
			MAC:                n.Properties.MacAddress,
			EnableIPForwarding: n.Properties.EnableIPForwarding,
		}
		if n.Properties.VirtualMachine != nil {
			it.VM = armIDTail(n.Properties.VirtualMachine.ID)
		}
		if n.Properties.NetworkSecurityGroup != nil {
			it.NSG = armIDTail(n.Properties.NetworkSecurityGroup.ID)
		}
		if len(n.Properties.IPConfigurations) > 0 {
			ic := n.Properties.IPConfigurations[0].Properties
			it.PrivateIP = ic.PrivateIPAddress
			if ic.Subnet != nil {
				it.Subnet = armIDTail(ic.Subnet.ID)
				it.VNet = armIDSegmentAfter(ic.Subnet.ID, "virtualNetworks")
			}
			if ic.PublicIPAddress != nil {
				it.PublicIPName = armIDTail(ic.PublicIPAddress.ID)
				if p, ok := pipByID[strings.ToLower(ic.PublicIPAddress.ID)]; ok {
					it.PublicIP = p.IP
					it.FQDN = p.FQDN
				}
			}
		}
		nics = append(nics, it)
		nicByID[strings.ToLower(n.ID)] = it
	}

	// Parse VNets & Subnets
	type subnetItem struct {
		Name              string   `json:"name"`
		VNet              string   `json:"vnet"`
		AddressPrefix     string   `json:"addressPrefix"`
		NSG               string   `json:"nsg"`
		RouteTable        string   `json:"routeTable"`
		ConnectedNICs     []string `json:"connectedNics"`
		ProvisioningState string   `json:"provisioningState"`
	}
	type vnetItem struct {
		Name            string       `json:"name"`
		ID              string       `json:"id"`
		Location        string       `json:"location"`
		AddressPrefixes []string     `json:"addressPrefixes"`
		Subnets         []subnetItem `json:"subnets"`
	}
	var vnets []vnetItem
	var rawVNets struct {
		Value []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Location   string `json:"location"`
			Properties struct {
				AddressSpace struct {
					AddressPrefixes []string `json:"addressPrefixes"`
				} `json:"addressSpace"`
				Subnets []struct {
					Name       string `json:"name"`
					Properties struct {
						AddressPrefix        string `json:"addressPrefix"`
						ProvisioningState    string `json:"provisioningState"`
						NetworkSecurityGroup *struct{ ID string `json:"id"` } `json:"networkSecurityGroup"`
						RouteTable           *struct{ ID string `json:"id"` } `json:"routeTable"`
						IPConfigurations     []struct{ ID string `json:"id"` } `json:"ipConfigurations"`
					} `json:"properties"`
				} `json:"subnets"`
			} `json:"properties"`
		} `json:"value"`
	}
	_ = json.Unmarshal(res["vnets"].body, &rawVNets)
	for _, vn := range rawVNets.Value {
		vi := vnetItem{
			Name:            vn.Name,
			ID:              vn.ID,
			Location:        vn.Location,
			AddressPrefixes: vn.Properties.AddressSpace.AddressPrefixes,
			Subnets:         []subnetItem{},
		}
		for _, sn := range vn.Properties.Subnets {
			si := subnetItem{
				Name:              sn.Name,
				VNet:              vn.Name,
				AddressPrefix:     sn.Properties.AddressPrefix,
				ProvisioningState: sn.Properties.ProvisioningState,
				ConnectedNICs:     []string{},
			}
			if sn.Properties.NetworkSecurityGroup != nil {
				si.NSG = armIDTail(sn.Properties.NetworkSecurityGroup.ID)
			}
			if sn.Properties.RouteTable != nil {
				si.RouteTable = armIDTail(sn.Properties.RouteTable.ID)
			}
			for _, ic := range sn.Properties.IPConfigurations {
				if nn := armIDSegmentAfter(ic.ID, "networkInterfaces"); nn != "" {
					si.ConnectedNICs = append(si.ConnectedNICs, nn)
				}
			}
			vi.Subnets = append(vi.Subnets, si)
		}
		vnets = append(vnets, vi)
	}

	// Parse NSGs
	type nsgRule struct {
		Name                     string `json:"name"`
		Priority                 int    `json:"priority"`
		Direction                string `json:"direction"`
		Access                   string `json:"access"`
		Protocol                 string `json:"protocol"`
		SourceAddressPrefix      string `json:"sourceAddressPrefix"`
		SourcePortRange          string `json:"sourcePortRange"`
		DestinationAddressPrefix string `json:"destinationAddressPrefix"`
		DestinationPortRange     string `json:"destinationPortRange"`
		Description              string `json:"description,omitempty"`
		Default                  bool   `json:"default,omitempty"`
	}
	type nsgItem struct {
		Name            string    `json:"name"`
		ID              string    `json:"id"`
		AttachedNICs    []string  `json:"attachedNics"`
		AttachedSubnets []string  `json:"attachedSubnets"`
		Rules           []nsgRule `json:"rules"`
		DefaultRules    []nsgRule `json:"defaultRules"`
	}
	var nsgs []nsgItem
	var rawNSGs struct {
		Value []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Properties struct {
				NetworkInterfaces []struct{ ID string `json:"id"` } `json:"networkInterfaces"`
				Subnets           []struct{ ID string `json:"id"` } `json:"subnets"`
				SecurityRules     []struct {
					Name       string `json:"name"`
					Properties struct {
						Priority                  int      `json:"priority"`
						Direction                 string   `json:"direction"`
						Access                    string   `json:"access"`
						Protocol                  string   `json:"protocol"`
						SourceAddressPrefix       string   `json:"sourceAddressPrefix"`
						SourceAddressPrefixes     []string `json:"sourceAddressPrefixes"`
						SourcePortRange           string   `json:"sourcePortRange"`
						DestinationAddressPrefix  string   `json:"destinationAddressPrefix"`
						DestinationAddressPrefixes []string `json:"destinationAddressPrefixes"`
						DestinationPortRange      string   `json:"destinationPortRange"`
						DestinationPortRanges     []string `json:"destinationPortRanges"`
						Description               string   `json:"description"`
					} `json:"properties"`
				} `json:"securityRules"`
				DefaultSecurityRules []struct {
					Name       string `json:"name"`
					Properties struct {
						Priority                 int    `json:"priority"`
						Direction                string `json:"direction"`
						Access                   string `json:"access"`
						Protocol                 string `json:"protocol"`
						SourceAddressPrefix      string `json:"sourceAddressPrefix"`
						SourcePortRange          string `json:"sourcePortRange"`
						DestinationAddressPrefix string `json:"destinationAddressPrefix"`
						DestinationPortRange     string `json:"destinationPortRange"`
						Description              string `json:"description"`
					} `json:"properties"`
				} `json:"defaultSecurityRules"`
			} `json:"properties"`
		} `json:"value"`
	}
	_ = json.Unmarshal(res["nsgs"].body, &rawNSGs)
	for _, g := range rawNSGs.Value {
		ni := nsgItem{
			Name:            g.Name,
			ID:              g.ID,
			AttachedNICs:    []string{},
			AttachedSubnets: []string{},
			Rules:           []nsgRule{},
			DefaultRules:    []nsgRule{},
		}
		for _, n := range g.Properties.NetworkInterfaces {
			ni.AttachedNICs = append(ni.AttachedNICs, armIDTail(n.ID))
		}
		for _, s := range g.Properties.Subnets {
			ni.AttachedSubnets = append(ni.AttachedSubnets, armIDSegmentAfter(s.ID, "virtualNetworks")+"/"+armIDTail(s.ID))
		}
		for _, sr := range g.Properties.SecurityRules {
			src := sr.Properties.SourceAddressPrefix
			if src == "" && len(sr.Properties.SourceAddressPrefixes) > 0 {
				src = strings.Join(sr.Properties.SourceAddressPrefixes, ", ")
			}
			dst := sr.Properties.DestinationAddressPrefix
			if dst == "" && len(sr.Properties.DestinationAddressPrefixes) > 0 {
				dst = strings.Join(sr.Properties.DestinationAddressPrefixes, ", ")
			}
			dp := sr.Properties.DestinationPortRange
			if dp == "" && len(sr.Properties.DestinationPortRanges) > 0 {
				dp = strings.Join(sr.Properties.DestinationPortRanges, ", ")
			}
			ni.Rules = append(ni.Rules, nsgRule{
				Name:                     sr.Name,
				Priority:                 sr.Properties.Priority,
				Direction:                sr.Properties.Direction,
				Access:                   sr.Properties.Access,
				Protocol:                 sr.Properties.Protocol,
				SourceAddressPrefix:      src,
				SourcePortRange:          sr.Properties.SourcePortRange,
				DestinationAddressPrefix: dst,
				DestinationPortRange:     dp,
				Description:              sr.Properties.Description,
			})
		}
		sort.Slice(ni.Rules, func(i, j int) bool {
			if ni.Rules[i].Direction != ni.Rules[j].Direction {
				return ni.Rules[i].Direction < ni.Rules[j].Direction
			}
			return ni.Rules[i].Priority < ni.Rules[j].Priority
		})
		for _, dr := range g.Properties.DefaultSecurityRules {
			ni.DefaultRules = append(ni.DefaultRules, nsgRule{
				Name:                     dr.Name,
				Priority:                 dr.Properties.Priority,
				Direction:                dr.Properties.Direction,
				Access:                   dr.Properties.Access,
				Protocol:                 dr.Properties.Protocol,
				SourceAddressPrefix:      dr.Properties.SourceAddressPrefix,
				SourcePortRange:          dr.Properties.SourcePortRange,
				DestinationAddressPrefix: dr.Properties.DestinationAddressPrefix,
				DestinationPortRange:     dr.Properties.DestinationPortRange,
				Description:              dr.Properties.Description,
				Default:                  true,
			})
		}
		nsgs = append(nsgs, ni)
	}

	// Parse Route Tables
	type rtRoute struct {
		Name             string `json:"name"`
		AddressPrefix    string `json:"addressPrefix"`
		NextHopType      string `json:"nextHopType"`
		NextHopIPAddress string `json:"nextHopIpAddress,omitempty"`
	}
	type rtItem struct {
		Name       string    `json:"name"`
		ID         string    `json:"id"`
		Subnets    []string  `json:"subnets"`
		Routes     []rtRoute `json:"routes"`
		DisableBGP bool      `json:"disableBgpRoutePropagation"`
	}
	var routeTables []rtItem
	var rawRTs struct {
		Value []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Properties struct {
				DisableBgpRoutePropagation bool `json:"disableBgpRoutePropagation"`
				Subnets                    []struct{ ID string `json:"id"` } `json:"subnets"`
				Routes                     []struct {
					Name       string `json:"name"`
					Properties struct {
						AddressPrefix    string `json:"addressPrefix"`
						NextHopType      string `json:"nextHopType"`
						NextHopIPAddress string `json:"nextHopIpAddress"`
					} `json:"properties"`
				} `json:"routes"`
			} `json:"properties"`
		} `json:"value"`
	}
	_ = json.Unmarshal(res["rts"].body, &rawRTs)
	for _, rt := range rawRTs.Value {
		it := rtItem{Name: rt.Name, ID: rt.ID, Subnets: []string{}, Routes: []rtRoute{}, DisableBGP: rt.Properties.DisableBgpRoutePropagation}
		for _, s := range rt.Properties.Subnets {
			it.Subnets = append(it.Subnets, armIDSegmentAfter(s.ID, "virtualNetworks")+"/"+armIDTail(s.ID))
		}
		for _, ro := range rt.Properties.Routes {
			it.Routes = append(it.Routes, rtRoute{
				Name:             ro.Name,
				AddressPrefix:    ro.Properties.AddressPrefix,
				NextHopType:      ro.Properties.NextHopType,
				NextHopIPAddress: ro.Properties.NextHopIPAddress,
			})
		}
		routeTables = append(routeTables, it)
	}

	// Parse VMs
	peerByVM := map[string]string{}
	for _, p := range getAllPeers() {
		if p.AzureVMName != "" {
			peerByVM[strings.ToLower(p.AzureVMName)] = p.ID
		}
		peerByVM[strings.ToLower(p.ID)] = p.ID
	}

	type vmItem struct {
		Name               string `json:"name"`
		ID                 string `json:"id"`
		Location           string `json:"location"`
		VMSize             string `json:"vmSize"`
		OSType             string `json:"osType"`
		ImageSKU           string `json:"imageSku"`
		PowerState         string `json:"powerState"`
		ProvisioningState  string `json:"provisioningState"`
		PrivateIP          string `json:"privateIp"`
		PublicIP           string `json:"publicIp"`
		FQDN               string `json:"fqdn,omitempty"`
		NIC                string `json:"nic"`
		VNet               string `json:"vnet"`
		Subnet             string `json:"subnet"`
		NSG                string `json:"nsg"`
		EnableIPForwarding bool   `json:"enableIPForwarding"`
		IsHub              bool   `json:"isHub"`
		NodeID             string `json:"nodeId,omitempty"`
	}
	var vms []vmItem
	var rawVMs struct {
		Value []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Location   string `json:"location"`
			Properties struct {
				ProvisioningState string `json:"provisioningState"`
				HardwareProfile   struct{ VMSize string `json:"vmSize"` } `json:"hardwareProfile"`
				StorageProfile    struct {
					OSDisk         struct{ OSType string `json:"osType"` } `json:"osDisk"`
					ImageReference struct{ SKU string `json:"sku"` } `json:"imageReference"`
				} `json:"storageProfile"`
				NetworkProfile struct {
					NetworkInterfaces []struct{ ID string `json:"id"` } `json:"networkInterfaces"`
				} `json:"networkProfile"`
				InstanceView *struct {
					Statuses []struct {
						Code          string `json:"code"`
						DisplayStatus string `json:"displayStatus"`
					} `json:"statuses"`
				} `json:"instanceView"`
			} `json:"properties"`
		} `json:"value"`
	}
	_ = json.Unmarshal(res["vms"].body, &rawVMs)
	// Fetch each VM's real-time power state via instanceView concurrently.
	powerStates := make([]string, len(rawVMs.Value))
	var ivWg sync.WaitGroup
	for i, v := range rawVMs.Value {
		ivWg.Add(1)
		go func(idx int, vmName string) {
			defer ivWg.Done()
			ivPath := fmt.Sprintf("%s/providers/Microsoft.Compute/virtualMachines/%s/instanceView?api-version=2024-03-01", base, vmName)
			b, _, err := armDo(ctx, "GET", ivPath, nil)
			if err != nil {
				return
			}
			var iv struct {
				Statuses []struct {
					Code          string `json:"code"`
					DisplayStatus string `json:"displayStatus"`
				} `json:"statuses"`
			}
			if json.Unmarshal(b, &iv) == nil {
				for _, st := range iv.Statuses {
					if strings.HasPrefix(st.Code, "PowerState/") {
						powerStates[idx] = st.DisplayStatus
						break
					}
				}
			}
		}(i, v.Name)
	}
	ivWg.Wait()

	for i, v := range rawVMs.Value {
		ps := powerStates[i]
		if ps == "" {
			ps = "unknown"
		}
		it := vmItem{
			Name:              v.Name,
			ID:                v.ID,
			Location:          v.Location,
			VMSize:            v.Properties.HardwareProfile.VMSize,
			OSType:            v.Properties.StorageProfile.OSDisk.OSType,
			ImageSKU:          v.Properties.StorageProfile.ImageReference.SKU,
			ProvisioningState: v.Properties.ProvisioningState,
			PowerState:        ps,
			IsHub:             strings.EqualFold(v.Name, localVM),
		}
		if it.IsHub {
			it.NodeID = "local"
		} else if pid, ok := peerByVM[strings.ToLower(v.Name)]; ok {
			it.NodeID = pid
		}
		if v.Properties.InstanceView != nil {
			for _, st := range v.Properties.InstanceView.Statuses {
				if strings.HasPrefix(st.Code, "PowerState/") {
					it.PowerState = st.DisplayStatus
				}
			}
		}
		if len(v.Properties.NetworkProfile.NetworkInterfaces) > 0 {
			if nic, ok := nicByID[strings.ToLower(v.Properties.NetworkProfile.NetworkInterfaces[0].ID)]; ok {
				it.NIC = nic.Name
				it.PrivateIP = nic.PrivateIP
				it.PublicIP = nic.PublicIP
				it.FQDN = nic.FQDN
				it.VNet = nic.VNet
				it.Subnet = nic.Subnet
				it.NSG = nic.NSG
				it.EnableIPForwarding = nic.EnableIPForwarding
				if it.IsHub {
					setLocalAzurePublicNet(nic.PublicIP, nic.FQDN)
				}
			}
		}
		vms = append(vms, it)
	}

	// Parse All Resources
	type resItem struct {
		Name     string `json:"name"`
		Type     string `json:"type"`
		Location string `json:"location"`
	}
	var resources []resItem
	var rawRes struct {
		Value []resItem `json:"value"`
	}
	_ = json.Unmarshal(res["resources"].body, &rawRes)
	resources = rawRes.Value
	sort.Slice(resources, func(i, j int) bool {
		if resources[i].Type != resources[j].Type {
			return resources[i].Type < resources[j].Type
		}
		return resources[i].Name < resources[j].Name
	})

	writeJSON(w, map[string]any{
		"available":      true,
		"subscriptionId": subID,
		"resourceGroup":  rg,
		"location":       loc,
		"localVm":        localVM,
		"errors":         errs,
		"vnets":          vnets,
		"nsgs":           nsgs,
		"routeTables":    routeTables,
		"nics":           nics,
		"publicIps":      pips,
		"vms":            vms,
		"resources":      resources,
		"gateway":        gw,
	})
}

// ---------- POST /api/azure/nsg/rule ----------

func handleAzureNSGRule(w http.ResponseWriter, r *http.Request) {
	subID, rg, _, _, ok := azureBaseScope()
	if !ok {
		httpErr(w, 400, "not running on an Azure VM")
		return
	}
	var q struct {
		Op                       string `json:"op"` // upsert | delete
		NSG                      string `json:"nsg"`
		Name                     string `json:"name"`
		Priority                 int    `json:"priority"`
		Direction                string `json:"direction"` // Inbound | Outbound
		Access                   string `json:"access"`    // Allow | Deny
		Protocol                 string `json:"protocol"`  // Tcp | Udp | Icmp | *
		SourceAddressPrefix      string `json:"sourceAddressPrefix"`
		SourcePortRange          string `json:"sourcePortRange"`
		DestinationAddressPrefix string `json:"destinationAddressPrefix"`
		DestinationPortRange     string `json:"destinationPortRange"`
		Description              string `json:"description"`
	}
	if err := readJSON(r, &q); err != nil {
		httpErr(w, 400, "bad request")
		return
	}
	if !reAzName.MatchString(q.NSG) || !reAzName.MatchString(q.Name) {
		httpErr(w, 400, "invalid NSG or rule name")
		return
	}
	path := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/networkSecurityGroups/%s/securityRules/%s?api-version=2023-11-01",
		subID, rg, q.NSG, q.Name)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	switch q.Op {
	case "delete":
		_, _, err := armDo(ctx, "DELETE", path, nil)
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	case "upsert":
		if q.Priority < 100 || q.Priority > 4096 {
			httpErr(w, 400, "Priority must be between 100 and 4096")
			return
		}
		if q.Direction != "Inbound" && q.Direction != "Outbound" {
			httpErr(w, 400, "Direction must be Inbound or Outbound")
			return
		}
		if q.Access != "Allow" && q.Access != "Deny" {
			httpErr(w, 400, "Access must be Allow or Deny")
			return
		}
		if q.Protocol == "" {
			q.Protocol = "*"
		}
		if q.SourceAddressPrefix == "" {
			q.SourceAddressPrefix = "*"
		}
		if q.SourcePortRange == "" {
			q.SourcePortRange = "*"
		}
		if q.DestinationAddressPrefix == "" {
			q.DestinationAddressPrefix = "*"
		}
		if q.DestinationPortRange == "" {
			q.DestinationPortRange = "*"
		}
		payload := map[string]any{
			"properties": map[string]any{
				"priority":                 q.Priority,
				"direction":                q.Direction,
				"access":                   q.Access,
				"protocol":                 q.Protocol,
				"sourceAddressPrefix":      q.SourceAddressPrefix,
				"sourcePortRange":          q.SourcePortRange,
				"destinationAddressPrefix": q.DestinationAddressPrefix,
				"destinationPortRange":     q.DestinationPortRange,
				"description":              q.Description,
			},
		}
		_, _, err := armDo(ctx, "PUT", path, payload)
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	default:
		httpErr(w, 400, "invalid operation")
	}
}

// ---------- POST /api/azure/route ----------

func handleAzureRoute(w http.ResponseWriter, r *http.Request) {
	subID, rg, _, _, ok := azureBaseScope()
	if !ok {
		httpErr(w, 400, "not running on an Azure VM")
		return
	}
	var q struct {
		Op               string `json:"op"` // upsert | delete
		RouteTable       string `json:"routeTable"`
		Name             string `json:"name"`
		AddressPrefix    string `json:"addressPrefix"`
		NextHopType      string `json:"nextHopType"`
		NextHopIPAddress string `json:"nextHopIpAddress"`
	}
	if err := readJSON(r, &q); err != nil {
		httpErr(w, 400, "bad request")
		return
	}
	if !reAzName.MatchString(q.RouteTable) || !reAzName.MatchString(q.Name) {
		httpErr(w, 400, "invalid route table or route name")
		return
	}
	path := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/routeTables/%s/routes/%s?api-version=2023-11-01",
		subID, rg, q.RouteTable, q.Name)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	switch q.Op {
	case "delete":
		_, _, err := armDo(ctx, "DELETE", path, nil)
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	case "upsert":
		if _, _, err := net.ParseCIDR(q.AddressPrefix); err != nil {
			httpErr(w, 400, "AddressPrefix must be a valid CIDR (e.g. 0.0.0.0/0)")
			return
		}
		props := map[string]any{
			"addressPrefix": q.AddressPrefix,
			"nextHopType":   q.NextHopType,
		}
		if q.NextHopType == "VirtualAppliance" {
			if net.ParseIP(q.NextHopIPAddress) == nil {
				httpErr(w, 400, "VirtualAppliance requires a valid NextHopIPAddress (e.g. 10.0.0.4)")
				return
			}
			props["nextHopIpAddress"] = q.NextHopIPAddress
		}
		_, _, err := armDo(ctx, "PUT", path, map[string]any{"properties": props})
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	default:
		httpErr(w, 400, "invalid operation")
	}
}

// ---------- POST /api/azure/subnet ----------

func handleAzureSubnet(w http.ResponseWriter, r *http.Request) {
	subID, rg, _, _, ok := azureBaseScope()
	if !ok {
		httpErr(w, 400, "not running on an Azure VM")
		return
	}
	var q struct {
		Op            string `json:"op"` // upsert | delete
		VNet          string `json:"vnet"`
		Name          string `json:"name"`
		AddressPrefix string `json:"addressPrefix"`
		NSG           string `json:"nsg"`
		RouteTable    string `json:"routeTable"`
	}
	if err := readJSON(r, &q); err != nil {
		httpErr(w, 400, "bad request")
		return
	}
	if !reAzName.MatchString(q.VNet) || !reAzName.MatchString(q.Name) {
		httpErr(w, 400, "invalid VNet or subnet name")
		return
	}
	path := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/virtualNetworks/%s/subnets/%s?api-version=2023-11-01",
		subID, rg, q.VNet, q.Name)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	switch q.Op {
	case "delete":
		_, _, err := armDo(ctx, "DELETE", path, nil)
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	case "upsert":
		if _, _, err := net.ParseCIDR(q.AddressPrefix); err != nil {
			httpErr(w, 400, "AddressPrefix must be a valid CIDR (e.g. 10.0.2.0/24)")
			return
		}
		props := map[string]any{"addressPrefix": q.AddressPrefix}
		if q.NSG != "" {
			if !reAzName.MatchString(q.NSG) {
				httpErr(w, 400, "invalid NSG name")
				return
			}
			props["networkSecurityGroup"] = map[string]string{
				"id": fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/networkSecurityGroups/%s", subID, rg, q.NSG),
			}
		}
		if q.RouteTable != "" {
			if !reAzName.MatchString(q.RouteTable) {
				httpErr(w, 400, "invalid route table name")
				return
			}
			props["routeTable"] = map[string]string{
				"id": fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/routeTables/%s", subID, rg, q.RouteTable),
			}
		}
		_, _, err := armDo(ctx, "PUT", path, map[string]any{"properties": props})
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	default:
		httpErr(w, 400, "invalid operation")
	}
}

// ---------- POST /api/azure/nic/forwarding ----------

func handleAzureNICForwarding(w http.ResponseWriter, r *http.Request) {
	subID, rg, _, _, ok := azureBaseScope()
	if !ok {
		httpErr(w, 400, "not running on an Azure VM")
		return
	}
	var q struct {
		NIC     string `json:"nic"`
		Enabled bool   `json:"enabled"`
	}
	if err := readJSON(r, &q); err != nil || !reAzName.MatchString(q.NIC) {
		httpErr(w, 400, "invalid NIC name")
		return
	}
	path := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/networkInterfaces/%s?api-version=2023-11-01",
		subID, rg, q.NIC)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	b, _, err := armDo(ctx, "GET", path, nil)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	props, _ := raw["properties"].(map[string]any)
	if props == nil {
		httpErr(w, 500, "failed to read NIC properties")
		return
	}
	props["enableIPForwarding"] = q.Enabled
	payload := map[string]any{
		"location":   raw["location"],
		"properties": props,
	}
	_, _, err = armDo(ctx, "PUT", path, payload)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// ---------- POST /api/azure/vm/action ----------

func handleAzureVMAction(w http.ResponseWriter, r *http.Request) {
	subID, rg, _, _, ok := azureBaseScope()
	if !ok {
		httpErr(w, 400, "not running on an Azure VM")
		return
	}
	var q struct {
		VM string `json:"vm"`
		Op string `json:"op"` // start | restart | deallocate | powerOff
	}
	if err := readJSON(r, &q); err != nil || !reAzName.MatchString(q.VM) {
		httpErr(w, 400, "invalid VM name")
		return
	}
	switch q.Op {
	case "start", "restart", "deallocate", "powerOff":
	default:
		httpErr(w, 400, "invalid VM action")
		return
	}
	path := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/virtualMachines/%s/%s?api-version=2024-03-01",
		subID, rg, q.VM, q.Op)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	_, _, err := armDo(ctx, "POST", path, nil)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// ---------- POST /api/azure/gateway ----------

func handleAzureGateway(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Op      string `json:"op"` // ip_forward | probe
		Enabled bool   `json:"enabled"`
		Host    string `json:"host"`
		Port    int    `json:"port"`
	}
	if err := readJSON(r, &q); err != nil {
		httpErr(w, 400, "bad request")
		return
	}
	switch q.Op {
	case "ip_forward":
		val := "0"
		if q.Enabled {
			val = "1"
		}
		out, err := runCmd(10*time.Second, "sysctl", "-w", "net.ipv4.ip_forward="+val)
		if err != nil {
			httpErr(w, 500, strings.TrimSpace(out+" "+err.Error()))
			return
		}
		_ = os.WriteFile("/etc/sysctl.d/99-tns-forward.conf", []byte("net.ipv4.ip_forward = "+val+"\n"), 0o644)
		writeJSON(w, map[string]any{"ok": true, "ipForward": q.Enabled})
	case "probe":
		host := strings.TrimSpace(q.Host)
		if net.ParseIP(host) == nil {
			httpErr(w, 400, "invalid IP address")
			return
		}
		res := map[string]any{"host": host, "port": q.Port}
		if q.Port > 0 && q.Port <= 65535 {
			t0 := time.Now()
			conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(q.Port)), 2500*time.Millisecond)
			res["tcpMs"] = time.Since(t0).Milliseconds()
			if err != nil {
				res["tcpOk"] = false
				res["tcpErr"] = err.Error()
			} else {
				res["tcpOk"] = true
				_ = conn.Close()
			}
		}
		if hasCmd("ping") {
			out, err := runCmd(5*time.Second, "ping", "-c", "2", "-W", "1", host)
			res["pingOk"] = err == nil
			res["pingOut"] = strings.TrimSpace(out)
		}
		writeJSON(w, res)
	default:
		httpErr(w, 400, "invalid operation")
	}
}
