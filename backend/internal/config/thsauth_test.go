package config

import "testing"

func TestTHSCredentialPairValidation(testingT *testing.T) {
	testingT.Setenv("SHADOWFLOW_THS_AUTO_LOGIN", "false")
	testingT.Setenv("SHADOWFLOW_THS_CREDENTIALS_FILE", "")
	testingT.Setenv("SHADOWFLOW_THS_SESSION_FILE", "")
	testingT.Setenv("SHADOWFLOW_THS_HEADLESS_BROWSER", "")
	testingT.Setenv("SHADOWFLOW_THS_USERNAME", "")
	testingT.Setenv("SHADOWFLOW_THS_PASSWORD", "")
	cfg, err := Load()
	if err != nil || cfg.THSAutoLogin {
		testingT.Fatalf("default account login state changed: %v", err)
	}
	// A username without a password is always a misconfiguration.
	testingT.Setenv("SHADOWFLOW_THS_AUTO_LOGIN", "true")
	testingT.Setenv("SHADOWFLOW_THS_USERNAME", "fixture-user")
	if _, err := Load(); err == nil {
		testingT.Fatal("username without password accepted")
	}
	testingT.Setenv("SHADOWFLOW_THS_PASSWORD", "fixture-secret")
	testingT.Setenv("SHADOWFLOW_THS_CREDENTIALS_FILE", "/private/credentials.json")
	if _, err := Load(); err == nil {
		testingT.Fatal("environment and file credentials accepted together")
	}
	testingT.Setenv("SHADOWFLOW_THS_CREDENTIALS_FILE", "")
	testingT.Setenv("SHADOWFLOW_THS_SESSION_FILE", "/private/session.json")
	testingT.Setenv("SHADOWFLOW_THS_HEADLESS_BROWSER", "/private/chrome")
	cfg, err = Load()
	if err != nil || !cfg.THSAutoLogin || cfg.THSUsername != "fixture-user" || cfg.THSHeadlessBrowser != "/private/chrome" {
		testingT.Fatalf("explicit login configuration failed: %v", err)
	}
}

func TestTHSAutoLoginAcceptsEnvironmentCredentialsWithoutSessionFile(testingT *testing.T) {
	testingT.Setenv("SHADOWFLOW_THS_AUTO_LOGIN", "true")
	testingT.Setenv("SHADOWFLOW_THS_CREDENTIALS_FILE", "")
	testingT.Setenv("SHADOWFLOW_THS_SESSION_FILE", "")
	testingT.Setenv("SHADOWFLOW_THS_HEADLESS_BROWSER", "/usr/bin/chromium")
	testingT.Setenv("SHADOWFLOW_THS_USERNAME", "")
	testingT.Setenv("SHADOWFLOW_THS_PASSWORD", "")
	testingT.Setenv("SHADOWFLOW_THS_CHROME_ARGS", "")
	if _, err := Load(); err != nil {
		testingT.Fatalf("session-only auto-login without credentials must be allowed: %v", err)
	}
	testingT.Setenv("SHADOWFLOW_THS_USERNAME", "fixture-user")
	if _, err := Load(); err == nil {
		testingT.Fatal("username without password accepted")
	}
	testingT.Setenv("SHADOWFLOW_THS_PASSWORD", "fixture-secret")
	testingT.Setenv("SHADOWFLOW_THS_CREDENTIALS_FILE", "/private/credentials.json")
	if _, err := Load(); err == nil {
		testingT.Fatal("environment and file credentials accepted together")
	}
	testingT.Setenv("SHADOWFLOW_THS_CREDENTIALS_FILE", "")
	testingT.Setenv("SHADOWFLOW_THS_CHROME_ARGS", "--no-sandbox --disable-dev-shm-usage")
	cfg, err := Load()
	if err != nil {
		testingT.Fatal(err)
	}
	if cfg.THSUsername != "fixture-user" || cfg.THSPassword != "fixture-secret" ||
		cfg.THSChromeArgs != "--no-sandbox --disable-dev-shm-usage" || cfg.THSSessionFile != "" {
		testingT.Fatal("unexpected environment configuration")
	}
}
