package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"syscall"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/term"
)

const version = "0.3.0"

func main() {
	elevate := flag.Bool("sudo", false, "Run with administrator permissions (sudo prompts in this terminal)")
	show := flag.Bool("version", false, "Print version")
	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(), "Sweep — disk explorer\n\nUsage: sweep [--sudo] [path]\n\nInteractive disk explorer and cleanup. Defaults to your home folder.\nDeletion is permanent and always requires interactive confirmation.\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *show {
		fmt.Println("sweep " + version)
		return
	}
	if flag.NArg() > 1 {
		flag.Usage()
		os.Exit(2)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fatal(err)
	}
	if os.Geteuid() == 0 && os.Getenv("SUDO_UID") != "" {
		original, lookupErr := user.LookupId(os.Getenv("SUDO_UID"))
		if lookupErr != nil {
			fatal(lookupErr)
		}
		home = original.HomeDir
	}
	root := home
	if flag.NArg() == 1 {
		root = flag.Arg(0)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		fatal(err)
	}
	if *elevate && os.Geteuid() != 0 {
		executable, exeErr := os.Executable()
		if exeErr != nil {
			fatal(exeErr)
		}
		if execErr := syscall.Exec("/usr/bin/sudo", []string{"sudo", "--", executable, "--", root}, os.Environ()); execErr != nil {
			fatal(execErr)
		}
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		fatal(err)
	}
	info, err := os.Stat(root)
	if err != nil {
		fatal(err)
	}
	if !info.IsDir() {
		fatal(fmt.Errorf("choose a directory to scan"))
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		fatal(fmt.Errorf("interactive terminal required; run sweep in your terminal"))
	}
	m := newModel(root, home)
	defer m.cancel()
	program := tea.NewProgram(m, tea.WithoutSignalHandler())
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-signals:
				program.Send(shutdownMsg{})
			case <-done:
				return
			}
		}
	}()
	_, runErr := program.Run()
	// Also drain workers if the terminal closes or the renderer returns an error.
	m.workers.Wait()
	close(done)
	signal.Stop(signals)
	for _, failure := range m.failures {
		fmt.Fprintln(os.Stderr, "sweep: deletion failed:", failure)
	}
	if runErr != nil {
		fatal(runErr)
	}
}
func fatal(err error) { fmt.Fprintln(os.Stderr, "sweep:", err); os.Exit(1) }
