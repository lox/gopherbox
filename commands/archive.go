package commands

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/afero"
)

func cmdTar(_ context.Context, args []string, ioCtx CommandIO) int {
	create := false
	extract := false
	list := false
	archiveName := ""
	files := []string{}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "-c":
			create = true
		case "-x":
			extract = true
		case "-t":
			list = true
		case "-f":
			if i+1 < len(args) {
				archiveName = args[i+1]
				i++
			}
		default:
			if strings.HasPrefix(arg, "-") {
				for _, ch := range arg[1:] {
					switch ch {
					case 'c':
						create = true
					case 'x':
						extract = true
					case 't':
						list = true
					case 'f':
						if i+1 < len(args) {
							archiveName = args[i+1]
							i++
						}
					}
				}
			} else {
				files = append(files, arg)
			}
		}
	}

	if archiveName == "" {
		writeErrf(ioCtx, "tar: missing archive name (-f)\n")
		return 1
	}
	archivePath := resolvePath(ioCtx.Cwd, archiveName)

	switch {
	case create:
		if len(files) == 0 {
			writeErrf(ioCtx, "tar: no files specified\n")
			return 1
		}
		out, err := ioCtx.Fs.OpenFile(archivePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			writeErrf(ioCtx, "tar: %v\n", err)
			return 1
		}
		defer out.Close()

		tw := tar.NewWriter(out)
		defer tw.Close()

		for _, file := range files {
			abs := resolvePath(ioCtx.Cwd, file)
			if err := addPathToTar(ioCtx.Fs, tw, abs, filepath.Clean(file)); err != nil {
				writeErrf(ioCtx, "tar: %v\n", err)
				return 1
			}
		}
		return 0

	case extract:
		in, err := ioCtx.Fs.Open(archivePath)
		if err != nil {
			writeErrf(ioCtx, "tar: %v\n", err)
			return 1
		}
		defer in.Close()

		tr := tar.NewReader(in)
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				writeErrf(ioCtx, "tar: %v\n", err)
				return 1
			}

			target := resolvePath(ioCtx.Cwd, hdr.Name)
			switch hdr.Typeflag {
			case tar.TypeDir:
				if err := ioCtx.Fs.MkdirAll(target, os.FileMode(hdr.Mode)); err != nil {
					writeErrf(ioCtx, "tar: %v\n", err)
					return 1
				}
			case tar.TypeReg:
				if err := ioCtx.Fs.MkdirAll(filepath.Dir(target), 0o755); err != nil {
					writeErrf(ioCtx, "tar: %v\n", err)
					return 1
				}
				out, err := ioCtx.Fs.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode))
				if err != nil {
					writeErrf(ioCtx, "tar: %v\n", err)
					return 1
				}
				if _, err := io.Copy(out, tr); err != nil {
					_ = out.Close()
					writeErrf(ioCtx, "tar: %v\n", err)
					return 1
				}
				_ = out.Close()
			}
		}
		return 0

	case list:
		in, err := ioCtx.Fs.Open(archivePath)
		if err != nil {
			writeErrf(ioCtx, "tar: %v\n", err)
			return 1
		}
		defer in.Close()

		tr := tar.NewReader(in)
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				writeErrf(ioCtx, "tar: %v\n", err)
				return 1
			}
			_, _ = fmt.Fprintln(ioCtx.Stdout, hdr.Name)
		}
		return 0
	}

	writeErrf(ioCtx, "tar: expected one of -c, -x, or -t\n")
	return 1
}

func addPathToTar(fs afero.Fs, tw *tar.Writer, absPath, headerPath string) error {
	info, err := fs.Stat(absPath)
	if err != nil {
		return err
	}

	if info.IsDir() {
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = strings.TrimSuffix(headerPath, "/") + "/"
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}

		entries, err := afero.ReadDir(fs, absPath)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := addPathToTar(fs, tw, filepath.Join(absPath, entry.Name()), filepath.Join(headerPath, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	}

	f, err := fs.Open(absPath)
	if err != nil {
		return err
	}
	defer f.Close()

	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	hdr.Name = headerPath
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

func cmdGzip(_ context.Context, args []string, ioCtx CommandIO) int {
	if len(args) == 0 {
		zw := gzip.NewWriter(ioCtx.Stdout)
		_, err := io.Copy(zw, ioCtx.Stdin)
		_ = zw.Close()
		if err != nil {
			writeErrf(ioCtx, "gzip: %v\n", err)
			return 1
		}
		return 0
	}

	exitCode := 0
	for _, arg := range args {
		inPath := resolvePath(ioCtx.Cwd, arg)
		outPath := inPath + ".gz"

		in, err := ioCtx.Fs.Open(inPath)
		if err != nil {
			writeErrf(ioCtx, "gzip: %s: %v\n", arg, err)
			exitCode = 1
			continue
		}
		out, err := ioCtx.Fs.OpenFile(outPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			_ = in.Close()
			writeErrf(ioCtx, "gzip: %s: %v\n", outPath, err)
			exitCode = 1
			continue
		}

		zw := gzip.NewWriter(out)
		_, err = io.Copy(zw, in)
		_ = zw.Close()
		_ = out.Close()
		_ = in.Close()
		if err != nil {
			writeErrf(ioCtx, "gzip: %s: %v\n", arg, err)
			exitCode = 1
			continue
		}
		_ = ioCtx.Fs.Remove(inPath)
	}

	return exitCode
}

func cmdGunzip(_ context.Context, args []string, ioCtx CommandIO) int {
	if len(args) == 0 {
		zr, err := gzip.NewReader(ioCtx.Stdin)
		if err != nil {
			writeErrf(ioCtx, "gunzip: %v\n", err)
			return 1
		}
		defer zr.Close()
		if _, err := io.Copy(ioCtx.Stdout, zr); err != nil {
			writeErrf(ioCtx, "gunzip: %v\n", err)
			return 1
		}
		return 0
	}

	exitCode := 0
	for _, arg := range args {
		inPath := resolvePath(ioCtx.Cwd, arg)
		outPath := strings.TrimSuffix(inPath, ".gz")
		if outPath == inPath {
			outPath = inPath + ".out"
		}

		in, err := ioCtx.Fs.Open(inPath)
		if err != nil {
			writeErrf(ioCtx, "gunzip: %s: %v\n", arg, err)
			exitCode = 1
			continue
		}
		zr, err := gzip.NewReader(in)
		if err != nil {
			_ = in.Close()
			writeErrf(ioCtx, "gunzip: %s: %v\n", arg, err)
			exitCode = 1
			continue
		}

		out, err := ioCtx.Fs.OpenFile(outPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			_ = zr.Close()
			_ = in.Close()
			writeErrf(ioCtx, "gunzip: %s: %v\n", outPath, err)
			exitCode = 1
			continue
		}

		_, err = io.Copy(out, zr)
		_ = out.Close()
		_ = zr.Close()
		_ = in.Close()
		if err != nil {
			writeErrf(ioCtx, "gunzip: %s: %v\n", arg, err)
			exitCode = 1
			continue
		}
		_ = ioCtx.Fs.Remove(inPath)
	}
	return exitCode
}

func cmdZcat(_ context.Context, args []string, ioCtx CommandIO) int {
	if len(args) == 0 {
		return cmdGunzip(context.Background(), nil, ioCtx)
	}

	exitCode := 0
	for _, arg := range args {
		inPath := resolvePath(ioCtx.Cwd, arg)
		in, err := ioCtx.Fs.Open(inPath)
		if err != nil {
			writeErrf(ioCtx, "zcat: %s: %v\n", arg, err)
			exitCode = 1
			continue
		}
		zr, err := gzip.NewReader(in)
		if err != nil {
			_ = in.Close()
			writeErrf(ioCtx, "zcat: %s: %v\n", arg, err)
			exitCode = 1
			continue
		}
		if _, err := io.Copy(ioCtx.Stdout, zr); err != nil {
			writeErrf(ioCtx, "zcat: %s: %v\n", arg, err)
			exitCode = 1
		}
		_ = zr.Close()
		_ = in.Close()
	}
	return exitCode
}

func cmdCurl(ctx context.Context, args []string, ioCtx CommandIO) int {
	if ioCtx.Network == nil {
		writeErrf(ioCtx, "curl: network is disabled\n")
		return 1
	}

	method := "GET"
	headers := http.Header{}
	body := ""
	url := ""
	headOnly := false
	outputPath := ""

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-X", "--request":
			if i+1 < len(args) {
				method = strings.ToUpper(args[i+1])
				i++
			}
		case "-H", "--header":
			if i+1 < len(args) {
				h := strings.SplitN(args[i+1], ":", 2)
				if len(h) == 2 {
					headers.Add(strings.TrimSpace(h[0]), strings.TrimSpace(h[1]))
				}
				i++
			}
		case "-d", "--data":
			if i+1 < len(args) {
				body = args[i+1]
				i++
			}
		case "-I", "--head":
			headOnly = true
			method = "HEAD"
		case "-o", "--output":
			if i+1 < len(args) {
				outputPath = args[i+1]
				i++
			}
		default:
			if !strings.HasPrefix(args[i], "-") {
				url = args[i]
			}
		}
	}

	if url == "" {
		writeErrf(ioCtx, "curl: no URL specified\n")
		return 2
	}
	if !ioCtx.Network.URLAllowed(url) {
		writeErrf(ioCtx, "curl: URL not allowed: %s\n", url)
		return 1
	}
	if !ioCtx.Network.MethodAllowed(method) {
		writeErrf(ioCtx, "curl: method not allowed: %s\n", method)
		return 1
	}

	requestBody := io.Reader(nil)
	if body != "" {
		requestBody = strings.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, requestBody)
	if err != nil {
		writeErrf(ioCtx, "curl: %v\n", err)
		return 2
	}
	for k, vals := range headers {
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		writeErrf(ioCtx, "curl: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	if headOnly {
		_, _ = fmt.Fprintf(ioCtx.Stdout, "HTTP %d\n", resp.StatusCode)
		for k, vals := range resp.Header {
			for _, v := range vals {
				_, _ = fmt.Fprintf(ioCtx.Stdout, "%s: %s\n", k, v)
			}
		}
		return 0
	}

	out := ioCtx.Stdout
	if outputPath != "" {
		f, err := ioCtx.Fs.OpenFile(resolvePath(ioCtx.Cwd, outputPath), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			writeErrf(ioCtx, "curl: %v\n", err)
			return 1
		}
		defer f.Close()
		out = f
	}

	if _, err := io.Copy(out, resp.Body); err != nil {
		writeErrf(ioCtx, "curl: %v\n", err)
		return 1
	}

	return 0
}
