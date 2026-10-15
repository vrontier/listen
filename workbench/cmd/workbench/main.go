// Command workbench is the Listening Observatory Workbench: the member
// area, the catalogue and the passing on of analysis data. It talks to the
// engine process over the control API (docs/control-api.md).
//
//	workbench -db low.db -engine /run/listen-engine/control.sock -base-url http://low.home.arpa
//	workbench -db low.db admin -email someone@example.org -name "Some One"
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/vrontier/listen-workbench/internal/catalogue"
	"github.com/vrontier/listen-workbench/internal/engine"
	"github.com/vrontier/listen-workbench/internal/mail"
	"github.com/vrontier/listen-workbench/internal/secret"
	"github.com/vrontier/listen-workbench/internal/web"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	var (
		addr    = flag.String("addr", "127.0.0.1:8090", "HTTP listen address (behind the web server)")
		dbPath  = flag.String("db", "workbench.db", "catalogue database file")
		dataDir = flag.String("datasets", "", "directory of the runs' data sets (default: \"datasets\" next to the database)")
		engAddr = flag.String("engine", env("LOW_ENGINE", "/run/listen-engine/control.sock"),
			"the engine's control API: a Unix socket path, or http://127.0.0.1:port for development [env LOW_ENGINE]")
		baseURL  = flag.String("base-url", env("LOW_BASE_URL", "http://low.home.arpa"), "address of the Workbench as members reach it, for links in mails [env LOW_BASE_URL]")
		smtpAddr = flag.String("smtp", env("LOW_SMTP", ""), "mail server host:port; empty: mails are written to the log [env LOW_SMTP]")
		smtpFrom = flag.String("smtp-from", env("LOW_SMTP_FROM", ""), "sender address of mails [env LOW_SMTP_FROM]")
		smtpUser = flag.String("smtp-user", env("LOW_SMTP_USER", ""), "mail server login; the password is read from LOW_SMTP_PASSWORD.\nMembers' language-model keys are sealed with LOW_KEYS_SECRET (32 bytes in base64, as made by openssl rand -base64 32)")

		publishing = flag.Bool("publishing", env("LOW_PUBLISHING", "") == "on", "let members publish analyses at /research [env LOW_PUBLISHING=on]")
	)
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: workbench [flags]\n       workbench [flags] admin -email ADDRESS -name NAME\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	db, err := catalogue.Open(*dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if flag.Arg(0) == "admin" {
		createAdmin(db, *baseURL, flag.Args()[1:])
		return
	}

	var sender mail.Sender = mail.Log{}
	if *smtpAddr != "" {
		if *smtpFrom == "" {
			log.Fatal("-smtp needs -smtp-from")
		}
		sender = mail.SMTP{Addr: *smtpAddr, From: *smtpFrom, User: *smtpUser, Password: os.Getenv("LOW_SMTP_PASSWORD")}
	} else {
		log.Printf("no mail server set: login links and invitations are written to this log")
	}

	if *dataDir == "" {
		*dataDir = filepath.Join(filepath.Dir(*dbPath), "datasets")
	}
	if err := os.MkdirAll(*dataDir, 0o750); err != nil {
		log.Fatal(err)
	}
	// The sealing key of members' language-model keys comes only from the
	// environment, never from a flag, which would show in the process list.
	var seal *secret.Box
	if v := os.Getenv("LOW_KEYS_SECRET"); v != "" {
		if seal, err = secret.Parse(v); err != nil {
			log.Fatalf("LOW_KEYS_SECRET: %v", err)
		}
	} else {
		log.Printf("LOW_KEYS_SECRET not set: members cannot save language-model keys")
	}
	srv, err := web.New(web.Config{BaseURL: *baseURL, DatasetDir: *dataDir, Seal: seal, Publishing: *publishing}, db, engine.Dial(*engAddr), sender)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv.Start(ctx)

	hs := &http.Server{Addr: *addr, Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Printf("workbench on http://%s (as %s), engine at %s", *addr, *baseURL, *engAddr)
		if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	<-ctx.Done()
	shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hs.Shutdown(shut)
}

// createAdmin makes the first master admin and prints a login link, since
// there is nobody yet to send an invitation.
func createAdmin(db *catalogue.DB, baseURL string, args []string) {
	fs := flag.NewFlagSet("admin", flag.ExitOnError)
	email := fs.String("email", "", "address of the master admin")
	name := fs.String("name", "", "display name")
	fs.Parse(args)
	if *email == "" || *name == "" {
		fs.Usage()
		os.Exit(2)
	}
	if !mail.ValidAddress(*email) {
		log.Fatalf("%q is not a complete email address", *email)
	}
	ctx := context.Background()
	u, err := db.UserByEmail(ctx, *email)
	if errors.Is(err, catalogue.ErrNotFound) {
		u, err = db.CreateUser(ctx, *email, *name, true)
	}
	if err != nil {
		log.Fatal(err)
	}
	if !u.Admin {
		log.Fatalf("%s exists and is not an admin", *email)
	}
	token, err := db.LoginLink(ctx, u.ID, 24*time.Hour)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("master admin %s <%s>\nlog in within 24 hours: %s/login/%s\n", u.Name, u.Email, baseURL, token)
}
