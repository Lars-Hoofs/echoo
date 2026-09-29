// Command echoo runs the Echoo server and its administrative subcommands.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"echoo/internal/api"
	"echoo/internal/auth"
	"echoo/internal/automation"
	"echoo/internal/campaigns"
	"echoo/internal/config"
	"echoo/internal/contacts"
	"echoo/internal/csat"
	"echoo/internal/db"
	"echoo/internal/db/dbq"
	"echoo/internal/inbox"
	jobargs "echoo/internal/jobs"
	"echoo/internal/keyring"
	"echoo/internal/logging"
	"echoo/internal/mail"
	"echoo/internal/mail/imapsync"
	"echoo/internal/mail/ingest"
	"echoo/internal/mail/send"
	"echoo/internal/mailauth"
	"echoo/internal/metrics"
	"echoo/internal/retention"
	"echoo/internal/scan"
	"echoo/internal/storage"
	"echoo/internal/sysmail"
	"echoo/internal/webhooks"
	"echoo/web"
)

const usage = `usage: echoo <command>

commands:
  serve                                  run the server (the schema must be migrated)
  migrate                                apply pending migrations and exit (needs the schema owner's database role)
  admin create-owner --email E --name N  create the owner account, prints a temporary password
  admin reset-password --email E         reset any account's password, prints a temporary password
  admin rotate-keys                      re-encrypt stored secrets with the active key, then report which old keys are unused
  admin blobs --check                    report blobs missing from storage and files nothing refers to
  admin blobs --delete-orphans [--yes]   delete files nothing refers to (lists them first; --yes confirms)
  genkey [id]                            print a new encryption key entry for ECHOO_ENCRYPTION_KEYS
  healthcheck                            exit 0 if the local server answers /healthz
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve()
	case "migrate":
		err = migrate()
	case "admin":
		err = admin(os.Args[2:])
	case "genkey":
		err = genkey(os.Args[2:])
	case "healthcheck":
		err = healthcheck()
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "echoo:", err)
		os.Exit(1)
	}
}

type app struct {
	cfg   *config.Config
	pool  *pgxpool.Pool
	keys  *keyring.Keyring
	store storage.Store
	auth  *auth.Service
}

// setup loads configuration and connects to the database. It never changes the schema: the
// server runs as a role that is not allowed to, and `echoo migrate` does that separately.
func setup(ctx context.Context) (*app, error) {
	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		return nil, err
	}
	slog.SetDefault(slog.New(logging.NewHandler(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))))

	keys, err := keyring.Parse(cfg.EncryptionKeys)
	if err != nil {
		return nil, fmt.Errorf("ECHOO_ENCRYPTION_KEYS: %w", err)
	}
	mail.SetMessageIDKey(keys.DeriveAll("message-id")...)
	send.RestrictRecipients(cfg.OutboundAllowedDomains)
	if len(cfg.OutboundAllowedDomains) > 0 {
		slog.Warn("outgoing mail is restricted", "allowed_domains", cfg.OutboundAllowedDomains)
	}
	store, err := storage.FromURL(cfg.Storage, cfg.S3AccessKey, cfg.S3SecretKey)
	if err != nil {
		return nil, fmt.Errorf("ECHOO_STORAGE: %w", err)
	}
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if err := db.CheckSchema(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	authSvc := auth.NewService(pool, auth.NewHasher(auth.DefaultArgon2, 2), keys)
	return &app{cfg: cfg, pool: pool, keys: keys, store: store, auth: authSvc}, nil
}

func serve() error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	a, err := setup(ctx)
	if err != nil {
		return err
	}
	defer a.pool.Close()
	for _, w := range a.cfg.Warnings() {
		slog.Warn(w)
	}

	providers, err := mailauth.NewProviders(mailauth.Credentials{
		GoogleClientID: a.cfg.OAuthGoogleClientID, GoogleClientSecret: a.cfg.OAuthGoogleClientSecret,
		MicrosoftClientID: a.cfg.OAuthMicrosoftClientID, MicrosoftClientSecret: a.cfg.OAuthMicrosoftClientSecret,
		MicrosoftTenant: a.cfg.OAuthMicrosoftTenant,
	})
	if err != nil {
		return fmt.Errorf("oauth providers: %w", err)
	}
	oauth := mailauth.NewManager(a.pool, a.keys, providers, strings.TrimSuffix(a.cfg.BaseURL.String(), "/"))
	sysmailer := sysmail.New(a.pool, a.keys, oauth, a.cfg, nil)

	var scanner scan.Scanner
	if a.cfg.ClamAVAddr != "" {
		scanner = scan.Clamd{Addr: a.cfg.ClamAVAddr}
	}

	workers := river.NewWorkers()
	river.AddWorker(workers, sysmail.NewWorker(sysmailer))
	river.AddWorker(workers, sysmail.NewNotificationWorker(a.pool, sysmailer, strings.TrimSuffix(a.cfg.BaseURL.String(), "/")))
	river.AddWorker(workers, ingest.NewWorker(ingest.Deps{Pool: a.pool, Store: a.store, Logger: slog.Default(), Scanner: scanner}))
	river.AddWorker(workers, send.NewWorker(send.Deps{Pool: a.pool, Storage: a.store, Keyring: a.keys, Tokens: oauth}))
	river.AddWorker(workers, inbox.NewWakeWorker(inbox.NewService(a.pool)))
	river.AddWorker(workers, webhooks.NewDeliverWorker(webhooks.DeliverDeps{Pool: a.pool, Keyring: a.keys}))
	river.AddWorker(workers, webhooks.NewFanoutWorker(a.pool))
	river.AddWorker(workers, webhooks.NewPurgeWorker(a.pool))
	automation.Register(workers, automation.NewEngine(automation.Deps{Pool: a.pool, Logger: slog.Default()}))
	river.AddWorker(workers, csat.NewWorker(csat.NewService(csat.Deps{
		Pool: a.pool, Signer: csat.NewSigner(a.keys.DeriveAll("csat-token")...), BaseURL: strings.TrimSuffix(a.cfg.BaseURL.String(), "/"),
	})))
	river.AddWorker(workers, campaigns.NewWorker(campaigns.NewService(campaigns.Deps{
		Pool: a.pool, BaseURL: strings.TrimSuffix(a.cfg.BaseURL.String(), "/"), MaxRate: a.cfg.CampaignRateLimit(),
	})))
	blobs, err := contacts.AsBlobs(a.store)
	if err != nil {
		return fmt.Errorf("contact blobs: %w", err)
	}
	river.AddWorker(workers, contacts.NewImportWorker(contacts.ImportDeps{Pool: a.pool, Blobs: blobs, IsFreeMailDomain: ingest.IsFreeMailDomain}))
	river.AddWorker(workers, contacts.NewExportWorker(a.pool, blobs))
	river.AddWorker(workers, contacts.NewPurgeWorker(a.pool, blobs))
	retentionSvc := retention.NewService(a.pool, blobs)
	river.AddWorker(workers, retention.NewPurgeWorker(retentionSvc))
	river.AddWorker(workers, retention.NewUploadsWorker(retentionSvc))
	jobs, err := river.NewClient(riverpgxv5.New(a.pool), &river.Config{
		Queues:  map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 10}},
		Workers: workers,
		Logger:  slog.Default(),
		PeriodicJobs: append([]*river.PeriodicJob{
			river.NewPeriodicJob(river.PeriodicInterval(time.Minute),
				func() (river.JobArgs, *river.InsertOpts) { return jobargs.WakeSnoozed{}, nil },
				&river.PeriodicJobOpts{RunOnStart: true}),
			river.NewPeriodicJob(river.PeriodicInterval(time.Minute),
				func() (river.JobArgs, *river.InsertOpts) { return jobargs.NotificationMail{}, nil },
				nil),
		}, append(append(append(append(append(webhooks.PeriodicJobs(), contacts.PeriodicJobs()...), automation.PeriodicJobs()...), csat.PeriodicJobs()...), retention.PeriodicJobs()...), campaigns.PeriodicJobs()...)...),
	})
	if err != nil {
		return fmt.Errorf("job queue: %w", err)
	}
	// Workers get a context that outlives the signal, so Stop can let running jobs finish.
	if err := jobs.Start(context.WithoutCancel(ctx)); err != nil {
		return fmt.Errorf("start job queue: %w", err)
	}

	mailboxes := imapsync.NewManager(imapsync.Deps{Pool: a.pool, Store: a.store, River: jobs, Keyring: a.keys, Tokens: oauth, Logger: slog.Default()})
	if err := mailboxes.Reload(ctx); err != nil {
		slog.Error("start mailbox sync", "err", err)
	}

	var reg *metrics.Registry
	if a.cfg.MetricsAddr != "" {
		ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", a.cfg.MetricsAddr)
		if err != nil {
			return fmt.Errorf("metrics listener: %w", err)
		}
		reg = metrics.New()
		reg.TrackPool(a.pool)
		go reg.NewCollector(a.pool).Run(ctx, 30*time.Second)
		go func() {
			slog.Info("metrics listening", "addr", a.cfg.MetricsAddr)
			if err := metrics.Serve(ctx, ln, reg.Handler()); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("metrics listener", "err", err)
			}
		}()
	}

	apiSrv := api.New(a.cfg, a.pool, a.auth, web.Dist(), api.WithMetrics(reg), api.WithKeyring(a.keys), api.WithMailboxReloader(mailboxes), api.WithJobs(jobs), api.WithStorage(a.store), api.WithScanner(scanner), api.WithOAuth(oauth), api.WithSysmail(sysmailer))
	go apiSrv.Realtime().Run(ctx)
	if reg != nil {
		reg.TrackStreams(apiSrv.Realtime().Streams)
	}

	httpSrv := &http.Server{
		Addr:              a.cfg.ListenAddr,
		Handler:           apiSrv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	// Open event streams would otherwise hold Shutdown until its deadline.
	httpSrv.RegisterOnShutdown(apiSrv.Realtime().Close)
	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", a.cfg.ListenAddr, "base_url", a.cfg.BaseURL.String())
		errCh <- httpSrv.ListenAndServe()
	}()

	var runErr error
	select {
	case runErr = <-errCh:
	case <-ctx.Done():
	}
	slog.Info("shutting down")
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancelShutdown()
	errs := []error{runErr}
	if runErr == nil {
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			errs = append(errs, fmt.Errorf("http shutdown: %w", err))
		} else if err := <-errCh; !errors.Is(err, http.ErrServerClosed) {
			errs = append(errs, err)
		}
	}
	if err := mailboxes.Shutdown(shutdownCtx); err != nil {
		errs = append(errs, fmt.Errorf("mailbox shutdown: %w", err))
	}
	if err := jobs.Stop(shutdownCtx); err != nil {
		errs = append(errs, fmt.Errorf("job queue shutdown: %w", err))
	}
	return errors.Join(errs...)
}

func migrate() error {
	url, err := config.LoadDatabaseURL(os.LookupEnv)
	if err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()
	return db.Migrate(ctx, pool)
}

func admin(args []string) error {
	if len(args) == 0 {
		return errors.New("missing admin command; see `echoo` for usage")
	}
	switch args[0] {
	case "rotate-keys":
		return rotateKeys(args[1:])
	case "blobs":
		return adminBlobs(args[1:])
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	email := fs.String("email", "", "account email address")
	name := fs.String("name", "", "display name (create-owner only)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *email == "" {
		return errors.New("--email is required")
	}
	ctx := context.Background()
	a, err := setup(ctx)
	if err != nil {
		return err
	}
	defer a.pool.Close()

	switch args[0] {
	case "create-owner":
		if *name == "" {
			return errors.New("--name is required")
		}
		temp, err := a.auth.CreateOwner(ctx, *email, *name)
		if err != nil {
			return fmt.Errorf("create owner (an owner may already exist): %w", err)
		}
		fmt.Printf("Owner %s created.\nTemporary password: %s\nIt must be changed at first sign-in, within 72 hours.\n", auth.NormalizeEmail(*email), temp)
	case "reset-password":
		// Operator recovery, also for the owner whom nobody can manage in the UI.
		user, err := dbq.New(a.pool).GetUserByEmail(ctx, auth.NormalizeEmail(*email))
		if err != nil {
			return fmt.Errorf("find user: %w", err)
		}
		temp, err := a.auth.ResetPassword(ctx, pgtype.UUID{}, user, auth.Client{})
		if err != nil {
			return err
		}
		fmt.Printf("Password of %s reset; all sessions were signed out.\nTemporary password: %s\nIt must be changed at first sign-in, within 72 hours.\n", user.Email, temp)
	default:
		return fmt.Errorf("unknown admin command %q; see `echoo` for usage", args[0])
	}
	return nil
}

func genkey(args []string) error {
	id := "k1"
	if len(args) > 0 {
		id = args[0]
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	entry := id + ":" + base64.StdEncoding.EncodeToString(b)
	if _, err := keyring.Parse(entry); err != nil {
		return err
	}
	fmt.Println(entry)
	return nil
}

func healthcheck() error {
	addr := os.Getenv("ECHOO_LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("ECHOO_LISTEN_ADDR: %w", err)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	// The host is fixed to loopback; only the port comes from configuration.
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz") //nolint:gosec,noctx // see above; a 3 s client timeout bounds the call
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz returned %d", resp.StatusCode)
	}
	return nil
}
