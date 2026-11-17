package main

import (
	"context"
	"crypto/rand"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"time"
)

//go:embed ui/*
var assets embed.FS

func main() {
	manual := flag.Bool("manual", false, "print the private local URL instead of opening the browser")
	flag.Parse()
	if err := run(*manual); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(manual bool) error {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	prefs, prefsErr := loadPreferences()
	a := &application{host: listener.Addr().String(), token: rand.Text(), prefs: prefs, prefsErr: prefsErr, stop: stop}
	static, err := fs.Sub(assets, "ui")
	if err != nil {
		return err
	}
	server := &http.Server{
		Handler: a.handler(http.FileServerFS(static)), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	address := "http://" + a.host + "/#" + a.token
	if manual {
		fmt.Println(address)
	} else if err := openBrowser(address); err != nil {
		return fmt.Errorf("не удалось открыть браузер: %w; запустите с параметром -manual", err)
	}
	go func() {
		<-ctx.Done()
		grace, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(grace)
	}()
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func openBrowser(address string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", address)
	case "darwin":
		cmd = exec.Command("/usr/bin/open", address)
	default:
		cmd = exec.Command("xdg-open", address)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
