package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// readFile reads the contents of filename, described by info.
// If in is non-nil, readFile reads directly from it.
// Otherwise, readFile opens and reads the file itself,
// with the number of concurrently-open files bounded by c.fdSem.
func (c *cliRunner) readFile(filename string, info fs.FileInfo, in io.Reader) ([]byte, error) {
	if in == nil {
		c.fdSem <- struct{}{}

		f, err := os.Open(filename)
		if err != nil {
			<-c.fdSem

			return nil, fmt.Errorf("open %s: %w", filename, err)
		}

		in = f

		defer func() {
			_ = f.Close()

			<-c.fdSem
		}()
	}

	size := -1
	if info != nil && info.Mode().IsRegular() && int64(int(info.Size())) == info.Size() {
		size = int(info.Size())
	}

	if size+1 <= 0 {
		src, err := io.ReadAll(in)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", filename, err)
		}

		return src, nil
	}

	src := make([]byte, size+1)

	n, err := io.ReadFull(in, src)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, fmt.Errorf("read %s: %w", filename, err)
	}

	if n < size {
		return nil, fmt.Errorf("error: size of %s changed during reading (from %d to %d bytes)", filename, size, n)
	} else if n > size {
		return nil, fmt.Errorf("error: size of %s changed during reading (from %d to >=%d bytes)", filename, size, len(src))
	}

	return src[:n], nil
}

// writeFile updates a file with the new formatted data using a temporary backup.
func (c *cliRunner) writeFile(
	filename string,
	orig, formatted []byte,
	perm fs.FileMode,
	r *reporter,
) error {
	bakname, err := c.backupFile(filename, orig, perm)
	if err != nil {
		return err
	}

	c.fdSem <- struct{}{}
	defer func() { <-c.fdSem }()

	fout, err := os.OpenFile(filename, os.O_WRONLY, perm)
	if err != nil {
		_ = os.Remove(bakname)

		return fmt.Errorf("open %s: %w", filename, err)
	}
	defer func() { _ = fout.Close() }()

	restoreFail := func(rerr error) {
		r.Warnf("bqfmt: %s: error restoring file to original: %v; backup in %s\n", filename, rerr, bakname)
	}

	n, err := fout.Write(formatted)
	if err == nil {
		err = fout.Truncate(int64(n))
	}

	if err != nil {
		if n == 0 {
			_ = os.Remove(bakname)

			return fmt.Errorf("write %s: %w", filename, err)
		}

		restoreOriginal(fout, orig, n, bakname, restoreFail)

		return fmt.Errorf("write %s: %w", filename, err)
	}

	if err := fout.Close(); err != nil {
		r.Warnf("bqfmt: %s: error closing file: %v; backup in %s\n", filename, err, bakname)

		return fmt.Errorf("close %s: %w", filename, err)
	}

	_ = os.Remove(bakname)

	return nil
}

func restoreOriginal(
	fout *os.File,
	orig []byte,
	written int,
	bakname string,
	restoreFail func(error),
) {
	no, erro := fout.WriteAt(orig, 0)
	if erro != nil {
		restoreFail(erro)

		return
	}

	if no < written {
		if erro = fout.Truncate(int64(no)); erro != nil {
			restoreFail(erro)

			return
		}
	}

	if erro := fout.Close(); erro != nil {
		restoreFail(erro)

		return
	}

	_ = os.Remove(bakname)
}

// backupFile writes data to a temporary sibling file with permissions perm.
func (c *cliRunner) backupFile(filename string, data []byte, perm fs.FileMode) (string, error) {
	c.fdSem <- struct{}{}
	defer func() { <-c.fdSem }()

	dir := filepath.Dir(filename)
	base := filepath.Base(filename)

	f, err := os.CreateTemp(dir, base+".*")
	if err != nil {
		return "", fmt.Errorf("create backup for %s: %w", filename, err)
	}

	bakname := f.Name()

	if chmodErr := f.Chmod(perm); chmodErr != nil {
		_ = f.Close()
		_ = os.Remove(bakname)

		return "", fmt.Errorf("chmod backup %s: %w", bakname, chmodErr)
	}

	_, err = f.Write(data)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}

	if err != nil {
		_ = os.Remove(bakname)

		return "", fmt.Errorf("write backup %s: %w", bakname, err)
	}

	return bakname, nil
}
