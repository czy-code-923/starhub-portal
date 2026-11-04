package main

import (
	"bufio"
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"starhub-portal/internal/portal"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(portal.Version)
		return
	}
	key, e := hex.DecodeString(os.Getenv("MASTER_KEY"))
	if e != nil || len(key) != 32 {
		log.Fatal("Set a persistent 64-character MASTER_KEY hex value")
	}
	origin := os.Getenv("PUBLIC_ORIGIN")
	if origin == "" {
		origin = "http://127.0.0.1:18088"
	}
	web := os.Getenv("WEB_DIR")
	if web == "" {
		web = "web/dist"
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	app, e := portal.Open(ctx, os.Getenv("DATABASE_URL"), key, origin, web)
	if e != nil {
		log.Fatal("Unable to initialize portal database; verify configuration and migrations")
	}
	defer app.DB.Close()
	app.WorkerToken = os.Getenv("WORKER_TOKEN")
	app.IPOrigin = os.Getenv("IP_ORIGIN")
	if app.IPOrigin != "" {
		if _, err := portal.ValidEntryOrigin(app.IPOrigin); err != nil {
			log.Fatal("Invalid IP_ORIGIN")
		}
	}
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "control-configure":
			if len(os.Args) != 3 {
				log.Fatal("Usage: portal control-configure HTTPS_ORIGIN; restricted key on stdin")
			}
			key, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			if e = app.ConfigureControl(ctx, os.Args[2], strings.TrimSpace(key)); e != nil {
				log.Fatal(e)
			}
			fmt.Println("Restricted control-plane connection configured and verified.")
			return
		case "admin-create", "admin-reset":
			if len(os.Args) != 3 {
				log.Fatal("Usage: portal admin-create EMAIL; provide password on stdin")
			}
			password, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			if os.Args[1] == "admin-reset" {
				e = app.ResetAdmin(ctx, os.Args[2], strings.TrimSpace(password))
			} else {
				e = app.Bootstrap(ctx, os.Args[2], strings.TrimSpace(password))
			}
			if e != nil {
				log.Fatal(e)
			}
			fmt.Println("Administrator created; no default/shared login credential is used.")
			return
		case "worker-once":
			if e = app.WorkOnce(ctx); e != nil {
				log.Fatal("Worker encountered an error")
			}
			return
		default:
			log.Fatal("Unknown command")
		}
	}
	go app.Run(ctx)
	listen := os.Getenv("LISTEN_ADDR")
	if listen == "" {
		listen = "127.0.0.1:18088"
	}
	srv := &http.Server{Addr: listen, Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	go func() {
		<-ctx.Done()
		stop, done := context.WithTimeout(context.Background(), 15*time.Second)
		defer done()
		_ = srv.Shutdown(stop)
	}()
	log.Printf("Starhub Portal %s listening on %s", portal.Version, listen)
	if e = srv.ListenAndServe(); e != nil && e != http.ErrServerClosed {
		log.Fatal("HTTP service stopped unexpectedly")
	}
}
