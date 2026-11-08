package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/roiding/shadowflow/internal/collector"
	"github.com/roiding/shadowflow/internal/config"
	"github.com/roiding/shadowflow/internal/datasource/thsdataq"
	"github.com/roiding/shadowflow/internal/repository/sqlite"
	"github.com/roiding/shadowflow/internal/thsauth"
	"github.com/roiding/shadowflow/internal/thsheadless"
)

// thsCredentialSource resolves credentials from compose environment variables
// or the hardened credentials file, whichever the deployment configured.
func thsCredentialSource(cfg config.Config) thsauth.CredentialSource {
	if cfg.THSUsername != "" {
		return thsauth.EnvCredentials(cfg.THSUsername, cfg.THSPassword)
	}
	return thsauth.FileCredentials(cfg.THSCredentialsFile)
}

func thsHeadlessOptions(cfg config.Config) (thsheadless.Options, error) {
	options := thsheadless.Options{Executable: cfg.THSHeadlessBrowser, Timeout: 30 * time.Second}
	if cfg.THSChromeArgs != "" {
		// Chrome launch flags are whitespace-separated simple tokens
		// (--no-sandbox, --disable-dev-shm-usage); no quoting is supported.
		options.ChromeArgs = strings.Fields(cfg.THSChromeArgs)
	}
	return options, nil
}

func newTHSAccountManager(cfg config.Config, store *sqlite.Store) (*thsauth.Manager, error) {
	var login thsauth.LoginFunc
	if cfg.THSCredentialsFile != "" || cfg.THSUsername != "" {
		source := thsCredentialSource(cfg)
		if cfg.THSHeadlessBrowser != "" {
			options, optionsErr := thsHeadlessOptions(cfg)
			if optionsErr != nil {
				return nil, optionsErr
			}
			browser, browserErr := thsheadless.New(options)
			if browserErr != nil {
				return nil, browserErr
			}
			login = thsauth.PasswordLoginWithRiskProviderSource(source, browser)
		} else {
			login = thsauth.PasswordLoginWithSource(source)
		}
	}
	options := thsauth.Options{
		SessionFile: cfg.THSSessionFile, AutoLogin: cfg.THSAutoLogin, Login: login,
		RequestTimeout: 20 * time.Second, RefreshTimeout: 90 * time.Second, Cooldown: 5 * time.Minute,
	}
	if cfg.THSSessionFile == "" {
		if store == nil {
			return nil, fmt.Errorf("THS database session store is not configured")
		}
		options.Session = store.THSSessionStore()
	}
	return thsauth.New(options)
}

func collectTHSAccount(cfg config.Config, store *sqlite.Store, task, date, codes string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	timeout := 2 * time.Minute
	if task == "ths-account-data" {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	manager, err := newTHSAccountManager(cfg, store)
	if err != nil {
		return err
	}
	defer manager.Close()
	switch task {
	case "ths-auth-status":
		return json.NewEncoder(os.Stdout).Encode(manager.Status())
	case "ths-login":
		if err := manager.Login(ctx); err != nil {
			_ = json.NewEncoder(os.Stdout).Encode(manager.Status())
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(manager.Status())
	case "ths-account-data":
		if codes == "" {
			return fmt.Errorf("ths-account-data requires -codes with THS identities such as 33:002578")
		}
		identities := strings.Split(codes, ",")
		payload, err := thsdataq.NewClient(manager).Fetch(ctx, date, identities)
		if err != nil {
			return err
		}
		fetchedAt := time.Now().UTC()
		if err := collector.StoreThsAccountBatch(ctx, store, date, identities, payload, fetchedAt); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(struct {
			TradeDate string          `json:"trade_date"`
			FetchedAt time.Time       `json:"fetched_at"`
			Source    string          `json:"source"`
			Payload   json.RawMessage `json:"payload"`
		}{date, fetchedAt, "ths_dataq", payload})
	default:
		return fmt.Errorf("unsupported THS account operation")
	}
}
