package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

type FileEntry struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Mode   string `json:"mode"`
	MTime  int64  `json:"mtime"`
	Dir    bool   `json:"dir"`
	Link   bool   `json:"link,omitempty"`
	Target string `json:"target,omitempty"`
	Owner  string `json:"owner,omitempty"`
}

func cleanPath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("missing path")
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("path must be absolute")
	}
	return filepath.Clean(p), nil
}

var protectedPaths = map[string]bool{"/": true, "/etc": true, "/usr": true, "/bin": true, "/sbin": true, "/boot": true,
	"/var": true, "/home": true, "/root": true, "/lib": true, "/lib64": true, "/proc": true, "/sys": true, "/dev": true,
	"/opt": true, "/srv": true, "/Users": true, "/System": true, "/Applications": true}

func ownerName(fi fs.FileInfo) string {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return lookupUser(int32(st.Uid))
}

func handleFilesList(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if p == "" {
		p, _ = os.UserHomeDir()
		if p == "" {
			p = "/"
		}
	}
	p, err := cleanPath(p)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	ents, err := os.ReadDir(p)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	procMu.Lock() // userMemo is shared with the process list
	defer procMu.Unlock()
	out := make([]FileEntry, 0, len(ents))
	for _, e := range ents {
		fe := FileEntry{Name: e.Name()}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			fe.Link = true
			fe.Target, _ = os.Readlink(filepath.Join(p, e.Name()))
			if ti, err := os.Stat(filepath.Join(p, e.Name())); err == nil {
				info = ti
			}
		}
		fe.Dir = info.IsDir()
		fe.Size = info.Size()
		fe.Mode = info.Mode().Perm().String()
		fe.MTime = info.ModTime().Unix()
		fe.Owner = ownerName(info)
		out = append(out, fe)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	home, _ := os.UserHomeDir()
	writeJSON(w, map[string]any{"path": p, "parent": filepath.Dir(p), "entries": out, "home": home})
}

// inline-unsafe types are served sandboxed so they can never run script on the panel's origin
var activeExt = map[string]bool{".html": true, ".htm": true, ".xhtml": true, ".svg": true, ".xml": true, ".js": true, ".mjs": true}

func handleFilesRaw(w http.ResponseWriter, r *http.Request) {
	p, err := cleanPath(r.URL.Query().Get("path"))
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	fi, err := os.Stat(p)
	if err != nil {
		httpErr(w, 404, err.Error())
		return
	}
	download := r.URL.Query().Get("dl") == "1"
	if fi.IsDir() {
		zipDir(w, p)
		return
	}
	f, err := os.Open(p)
	if err != nil {
		httpErr(w, 403, err.Error())
		return
	}
	defer f.Close()
	ext := strings.ToLower(filepath.Ext(p))
	ct := mime.TypeByExtension(ext)
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Security-Policy", "sandbox")
	if activeExt[ext] && !download {
		if ext == ".svg" {
			ct = "image/svg+xml"
		} else {
			ct = "text/plain; charset=utf-8"
		}
	}
	if ext == ".pdf" {
		w.Header().Del("Content-Security-Policy") // Chrome's PDF viewer refuses sandboxed documents
	}
	w.Header().Set("Content-Type", ct)
	disp := "inline"
	if download {
		disp = "attachment"
	}
	w.Header().Set("Content-Disposition", disp+"; filename*=UTF-8''"+urlEscape(filepath.Base(p)))
	http.ServeContent(w, r, filepath.Base(p), fi.ModTime(), f)
}

func urlEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.IndexByte("-._~", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func zipDir(w http.ResponseWriter, dir string) {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+urlEscape(filepath.Base(dir)+".zip"))
	zw := zip.NewWriter(w)
	defer zw.Close()
	base := filepath.Dir(dir)
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, _ := filepath.Rel(base, path)
		if d.IsDir() {
			_, _ = zw.Create(rel + "/")
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, _ := d.Info()
		hdr, _ := zip.FileInfoHeader(info)
		hdr.Name = rel
		hdr.Method = zip.Deflate
		fw, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		_, _ = io.Copy(fw, f)
		f.Close()
		return nil
	})
}

const maxTextSize = 5 << 20

func handleFilesText(w http.ResponseWriter, r *http.Request) {
	p, err := cleanPath(r.URL.Query().Get("path"))
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	fi, err := os.Stat(p)
	if err != nil || fi.IsDir() {
		httpErr(w, 404, "unable to open file")
		return
	}
	f, err := os.Open(p)
	if err != nil {
		httpErr(w, 403, err.Error())
		return
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, maxTextSize))
	head := b
	if len(head) > 8192 {
		head = head[:8192]
	}
	writeJSON(w, map[string]any{
		"content": string(b), "size": fi.Size(), "binary": bytes.IndexByte(head, 0) >= 0,
		"truncated": fi.Size() > maxTextSize, "writable": fi.Mode().Perm()&0o222 != 0,
	})
}

func handleFilesSave(w http.ResponseWriter, r *http.Request) {
	var q struct{ Path, Content string }
	if err := readJSON(r, &q); err != nil {
		httpErr(w, 400, "bad request")
		return
	}
	p, err := cleanPath(q.Path)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(p); err == nil {
		if fi.Size() > maxTextSize {
			httpErr(w, 400, "file is too large to edit in the browser")
			return
		}
		mode = fi.Mode().Perm()
	}
	if err := os.WriteFile(p, []byte(q.Content), mode); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func handleFilesUpload(w http.ResponseWriter, r *http.Request) {
	dir, err := cleanPath(r.URL.Query().Get("dir"))
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		httpErr(w, 400, "multipart expected")
		return
	}
	n := 0
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		name := filepath.Base(part.FileName())
		if name == "" || name == "." || name == ".." || name == "/" {
			continue
		}
		dst := filepath.Join(dir, name)
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		_, err = io.Copy(f, part)
		f.Close()
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		n++
	}
	writeJSON(w, map[string]any{"ok": true, "count": n})
}

func handleFilesOp(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Op    string   `json:"op"`
		Path  string   `json:"path"`
		To    string   `json:"to"`
		Paths []string `json:"paths"`
		Mode  string   `json:"mode"`
	}
	if err := readJSON(r, &q); err != nil {
		httpErr(w, 400, "bad request")
		return
	}
	switch q.Op {
	case "mkdir", "touch":
		p, err := cleanPath(q.Path)
		if err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		if q.Op == "mkdir" {
			err = os.Mkdir(p, 0o755)
		} else {
			var f *os.File
			f, err = os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
			if err == nil {
				f.Close()
			}
		}
		if err != nil {
			httpErr(w, 400, err.Error())
			return
		}
	case "rename":
		from, err1 := cleanPath(q.Path)
		to, err2 := cleanPath(q.To)
		if err1 != nil || err2 != nil || protectedPaths[from] {
			httpErr(w, 400, "invalid path")
			return
		}
		if _, err := os.Lstat(to); err == nil {
			httpErr(w, 409, "destination already exists")
			return
		}
		if err := os.Rename(from, to); err != nil {
			httpErr(w, 400, err.Error())
			return
		}
	case "chmod":
		p, err := cleanPath(q.Path)
		m, perr := strconv.ParseUint(q.Mode, 8, 32)
		if err != nil || perr != nil || m > 0o7777 {
			httpErr(w, 400, "invalid mode")
			return
		}
		if err := os.Chmod(p, os.FileMode(m)); err != nil {
			httpErr(w, 400, err.Error())
			return
		}
	case "delete":
		for _, raw := range q.Paths {
			p, err := cleanPath(raw)
			if err != nil || protectedPaths[p] {
				httpErr(w, 400, "cannot delete "+raw)
				return
			}
			if err := os.RemoveAll(p); err != nil {
				httpErr(w, 500, err.Error())
				return
			}
		}
	default:
		httpErr(w, 400, "invalid operation")
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
