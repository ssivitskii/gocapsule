package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"

	"github.com/ssivitskii/gocapsule/capsule"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	var err error
	switch args[0] {
	case "demo":
		err = runDemo(args[1:], stdout, stderr)
	case "inspect":
		err = runInspect(args[1:], stdout, stderr)
	case "verify":
		err = runVerify(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		printUsage(stderr)
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "gocapsule %s: %v\n", args[0], err)
		return 1
	}
	return 0
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: gocapsule <demo|inspect|verify> [options]")
}

func runDemo(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("demo", flag.ContinueOnError)
	flags.SetOutput(stderr)
	output := flags.String("output", "gocapsules", "private directory for the generated archive")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("demo does not accept positional arguments")
	}
	recorder, err := capsule.NewRecorder(capsule.Config{OutputDir: *output})
	if err != nil {
		return err
	}
	if err := recorder.Start(); err != nil {
		return err
	}
	defer recorder.Close()

	// These goroutines finish before the point-in-time profiles are taken, but
	// their scheduling history remains visible in the flight-recorder trace.
	var group sync.WaitGroup
	group.Add(256)
	for range 256 {
		go func() {
			defer group.Done()
			for range 64 {
				runtime.Gosched()
			}
		}()
	}
	group.Wait()
	result, err := recorder.Capture(context.Background(), "demo.goroutine-burst")
	if err != nil {
		return err
	}
	if result.Outcome != capsule.OutcomeCaptured {
		return fmt.Errorf("capture was %s", result.Outcome)
	}
	fmt.Fprintf(stdout, "created %s\n", result.Path)
	fmt.Fprintln(stdout, "verify before extracting: gocapsule verify <archive>")
	fmt.Fprintln(stdout, "then analyze trace.out with: go tool trace trace.out")
	fmt.Fprintln(stdout, "and profiles with: go tool pprof profiles/heap.pb.gz")
	return nil
}

func runInspect(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("inspect", flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return fmt.Errorf("inspect requires exactly one archive path")
	}
	manifest, err := capsule.Inspect(flags.Arg(0))
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(manifest)
}

func runVerify(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return fmt.Errorf("verify requires exactly one archive path")
	}
	manifest, err := capsule.Verify(flags.Arg(0))
	if err != nil {
		return err
	}
	if manifest == nil {
		return errors.New("verification returned no manifest")
	}
	fmt.Fprintf(stdout, "verified %s (%d payloads)\n", flags.Arg(0), len(manifest.Entries))
	return nil
}
