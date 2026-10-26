// founderos-sales-kit exports the same data-free package as the OS download.
package main

import (
	"flag"
	"fmt"
	"github.com/rhl/businessos-backend/internal/founderos/saleskit"
	"os"
)

func main() {
	out := flag.String("output", "", "new ZIP file to create (existing files are never overwritten)")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "usage: founderos-sales-kit -output /path/to/sales-kit.zip")
		os.Exit(2)
	}
	raw, err := saleskit.Bundle()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_, err = f.Write(raw)
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("Sales Kit %s: %d workflows exported to %s\n", saleskit.Version, len(saleskit.Catalog()), *out)
}
