// package-tar creates Unix release archives with executable binary permissions,
// including when the release is built on Windows.
package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: package-tar SOURCE_DIRECTORY OUTPUT.tar.gz")
		os.Exit(2)
	}
	if err := pack(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func pack(source, destination string) (err error) {
	out, err := os.Create(destination)
	if err != nil {
		return err
	}
	defer func() {
		if e := out.Close(); err == nil {
			err = e
		}
	}()
	gz := gzip.NewWriter(out)
	defer func() {
		if e := gz.Close(); err == nil {
			err = e
		}
	}()
	tw := tar.NewWriter(gz)
	defer func() {
		if e := tw.Close(); err == nil {
			err = e
		}
	}()
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported release entry: %s", path)
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(filepath.Dir(source), path)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		header.Mode = 0644
		if info.IsDir() || path == filepath.Join(source, "zagent") {
			header.Mode = 0755
		}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, input)
		closeErr := input.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}
