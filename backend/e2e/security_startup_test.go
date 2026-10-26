package e2e_test

// Security regression tests for the MongoDB-only start-up (S-03): credentials stay out of errors
// and logs, no second storage path is left, and the tests themselves only touch their own
// databases (docs/security/S03-11-mongo-only-hardening.md). They add to startup_test.go, which
// covers the defaults, the unreachable MongoDB and the ignored STORAGE variable.
//
// Every top-level identifier is prefixed with "securityStartup". The URIs below that must fail
// use 127.0.0.1:1, which refuses connections, so they need no server.
//
// A test that starts with t.Skip("SECURITY <id>: ...") pins behavior that does not hold yet; the
// follow-up named there removes the skip.

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"orch-test/backend/internal/bootstrap"
)

const (
	securityStartupUser     = "SecUserQ7"
	securityStartupPassword = "S3cr3tPwQ7"
)

// securityStartupFailure starts the application on uri and returns the start-up error. It fails
// the test if the start succeeds, or returns an App along with the error.
func securityStartupFailure(t *testing.T, uri string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	app, err := bootstrap.New(ctx, e2eConfig(uri, "orch"))
	if err == nil {
		_ = app.Close(context.Background())
		t.Fatal("bootstrap.New succeeded on a URI that must fail")
	}
	if app != nil {
		t.Error("bootstrap.New returned an App along with the error")
	}
	return err.Error()
}

// securityStartupRequireNone fails if text contains any of the secrets.
func securityStartupRequireNone(t *testing.T, text string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		// Case-insensitive: net/url lower-cases a scheme, and a user name can be taken for one.
		if strings.Contains(strings.ToLower(text), strings.ToLower(secret)) {
			t.Errorf("the start-up error contains %q:\n%s", secret, text)
		}
	}
}

// --- credentials in start-up errors ------------------------------------------------------------

// URIs that are wrong in a way the driver names without quoting the credentials: the error must
// hold neither the user nor the password.
func TestSecurityStartupMalformedURIsDoNotLeakUserinfo(t *testing.T) {
	cred := securityStartupUser + ":" + securityStartupPassword
	tests := map[string]string{
		"port out of range":          "mongodb://" + cred + "@127.0.0.1:99999/",
		"two @ signs":                "mongodb://" + cred + "@@127.0.0.1:1/",
		"colon in the password":      "mongodb://" + securityStartupUser + ":" + securityStartupPassword + ":x@127.0.0.1:1/",
		"unknown scheme":             "http://" + cred + "@127.0.0.1:1/",
		"unknown auth mechanism":     "mongodb://" + cred + "@127.0.0.1:1/?authMechanism=" + securityStartupPassword,
		"empty authSource":           "mongodb://" + cred + "@127.0.0.1:1/?authSource=",
		"bad boolean option":         "mongodb://" + cred + "@127.0.0.1:1/?tls=maybe",
		"bad escape in the user":     "mongodb://Sec%zzUserQ7:" + securityStartupPassword + "@127.0.0.1:1/",
		"bad escape in the password": "mongodb://" + securityStartupUser + ":S3cr3t%zzPwQ7@127.0.0.1:1/",
	}
	for name, uri := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			msg := securityStartupFailure(t, uri)
			securityStartupRequireNone(t, msg, securityStartupPassword, "S3cr3t", "SecUser", "Q7")
		})
	}
}

// SEC-S03-1: redactMongoURI parses the URI with net/url, which splits it differently from the
// driver when the user info holds an unescaped # or ?, or when the scheme is missing.
func TestSecurityStartupRedactionSurvivesUnescapedSpecialCharacters(t *testing.T) {
	t.Skip("SECURITY SEC-S03-1: fixed by follow-up 'Keep credentials out of MongoDB start-up errors'")

	tests := map[string]string{
		"# after a numeric password":    "mongodb://" + securityStartupUser + ":12345#" + securityStartupPassword + "@127.0.0.1:1/",
		"? after a numeric password":    "mongodb://" + securityStartupUser + ":12345?" + securityStartupPassword + "@127.0.0.1:1/",
		"# after the user":              "mongodb://" + securityStartupUser + "#" + securityStartupPassword + "@127.0.0.1:1/",
		"no scheme":                     securityStartupUser + ":" + securityStartupPassword + "@127.0.0.1:1",
		"user and password, no slashes": "mongodb:" + securityStartupUser + ":" + securityStartupPassword + "@127.0.0.1:1",
	}
	for name, uri := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			msg := securityStartupFailure(t, uri)
			securityStartupRequireNone(t, msg, securityStartupUser, securityStartupPassword, "12345")
		})
	}
}

// SEC-S03-2: the driver's URI errors are wrapped with %w and quote option values, so a secret in
// the query (a certificate key password) reaches the error when its percent-encoding is wrong.
func TestSecurityStartupDriverParseErrorsDoNotEchoQueryValues(t *testing.T) {
	t.Skip("SECURITY SEC-S03-2: fixed by follow-up 'Keep credentials out of MongoDB start-up errors'")

	tests := map[string]string{
		"certificate key password with a bad escape": "mongodb://127.0.0.1:1/?tlsCertificateKeyFilePassword=S3cr3t%zzPwQ7",
		"numeric option with a secret-looking value": "mongodb://127.0.0.1:1/?maxPoolSize=" + securityStartupPassword,
	}
	for name, uri := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			msg := securityStartupFailure(t, uri)
			securityStartupRequireNone(t, msg, "S3cr3t", "Q7")
		})
	}
}

// A test failure must not print MONGO_TEST_URI either: url.Parse errors quote their input.
func TestSecurityStartupMongoTestHostErrorsNeverQuoteTheURI(t *testing.T) {
	for name, uri := range map[string]string{
		"space in the password": "mongodb://" + securityStartupUser + ":pass " + securityStartupPassword + "@127.0.0.1:1/",
		"bad port":              "mongodb://" + securityStartupUser + ":" + securityStartupPassword + "@127.0.0.1:bad/",
		"bad escape":            "mongodb://" + securityStartupUser + ":" + securityStartupPassword + "%zz@127.0.0.1:1/",
		"no host":               "mongodb:///" + securityStartupPassword,
	} {
		_, err := secMongoHost(uri)
		if err == nil {
			t.Errorf("%s: secMongoHost accepted %q", name, uri)
			continue
		}
		for _, secret := range []string{securityStartupUser, securityStartupPassword, "mongodb://"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("%s: the error quotes %q: %v", name, secret, err)
			}
		}
	}
	if host, err := secMongoHost("mongodb://u:p@db.example:27017/x"); err != nil || host != "db.example:27017" {
		t.Errorf("secMongoHost of a valid URI = %q, %v; want db.example:27017", host, err)
	}
}

// --- credentials in the logs of a running server ---------------------------------------------------

// securityStartupWithAppName adds an appName option to a MongoDB URI. appName is harmless to the
// server and shows up nowhere unless the server prints its URI.
func securityStartupWithAppName(uri, name string) string {
	if strings.Contains(uri, "?") {
		return uri + "&appName=" + name
	}
	rest := uri[strings.Index(uri, "://")+3:]
	if !strings.Contains(rest, "/") {
		uri += "/"
	}
	return uri + "?appName=" + name
}

// A server that starts, serves and shuts down on a working MongoDB writes no URI to its
// output: the unreachable-MongoDB tests cover the failure path, this covers the start-up,
// migration, request and shutdown lines. The marker travels in the URI's query, which the
// redaction drops from errors; nothing else may print it either.
func TestSecurityStartupServerOutputNeverCarriesTheMongoURI(t *testing.T) {
	const marker = "SecretMarkerQ7d41c"
	name, _ := e2eTestDB(t)
	bin := startupBuildServer(t, t.TempDir())
	port := startupFreePort(t)

	cmd := exec.Command(bin)
	cmd.Env = append(startupCleanEnv(),
		"MONGO_URI="+securityStartupWithAppName(e2eMongoURI(t), marker),
		"MONGO_DB="+name,
		"LISTEN_HOST=127.0.0.1",
		"PORT="+port,
	)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the server: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	base := "http://127.0.0.1:" + port
	httpClient := &http.Client{Timeout: 3 * time.Second}
	ready := false
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline) && !ready; {
		select {
		case err := <-done:
			t.Fatalf("the server exited during start-up (%v):\n%s", err, out.String())
		default:
		}
		res, err := httpClient.Get(base + "/api/health")
		if err == nil {
			ready = res.StatusCode == http.StatusOK
			_ = res.Body.Close()
		}
		if !ready {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if !ready {
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("the server did not become healthy within 30s:\n%s", out.String())
	}
	if res, err := httpClient.Get(base + "/api/books"); err != nil {
		t.Errorf("GET /api/books: %v", err)
	} else {
		_ = res.Body.Close()
	}

	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("interrupt the server: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("the server did not shut down cleanly: %v", err)
		}
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("the server did not stop within 20s of SIGINT:\n%s", out.String())
	}

	log := out.String()
	if !strings.Contains(log, "listening") {
		t.Errorf("the output has no start-up line, so this test would not notice a leak:\n%s", log)
	}
	for _, leak := range []string{marker, "mongodb://", "mongodb+srv://"} {
		if strings.Contains(log, leak) {
			t.Errorf("the server output contains %q:\n%s", leak, log)
		}
	}
}

// --- no second storage -----------------------------------------------------------------------------

// There is one storage. Nothing may bring back a way to start without MongoDB: no Config field
// that picks a storage, no memory adapter, no STORAGE setting in the files that start the server.
func TestSecurityStartupNoFallbackStorageIsLeft(t *testing.T) {
	typ := reflect.TypeOf(bootstrap.Config{})
	for i := range typ.NumField() {
		lower := strings.ToLower(typ.Field(i).Name)
		for _, word := range []string{"storage", "memory", "inmem", "fallback"} {
			if strings.Contains(lower, word) {
				t.Errorf("bootstrap.Config has the field %s: there is one storage and nothing to choose", typ.Field(i).Name)
			}
		}
	}

	if _, err := os.Stat("../internal/catalog/adapter/memory"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("internal/catalog/adapter/memory exists again (stat error: %v)", err)
	}

	// No Go file imports a memory adapter. The architecture test names old paths in strings, and
	// this file names the needle.
	needle := "adapter" + "/memory"
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if slashed := filepath.ToSlash(path); slashed == "../internal/oas" || slashed == "../internal/archtest" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || filepath.Base(path) == "security_startup_test.go" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(raw), needle) {
			t.Errorf("%s mentions %s", path, needle)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	storage := regexp.MustCompile(`\bSTORAGE\b`)
	for _, rel := range []string{"../../docker-compose.yml", "../Dockerfile", "../.dockerignore", "../../Makefile"} {
		for _, l := range securityCatalogFile(t, rel) {
			if storage.MatchString(l) {
				t.Errorf("%s still sets or mentions STORAGE: %q", rel, strings.TrimSpace(l))
			}
		}
	}
}

// The tooling refuses to run the checks without a MongoDB to test against, instead of passing
// green with nothing tested.
func TestSecurityStartupCheckScriptRefusesWithoutMongoTestURI(t *testing.T) {
	var base []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "MONGO_TEST_URI=") {
			base = append(base, kv)
		}
	}
	for name, extra := range map[string][]string{"unset": nil, "empty": {"MONGO_TEST_URI="}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "sh", "../check.sh")
			cmd.Env = append(slices.Clone(base), extra...)
			out, err := cmd.CombinedOutput()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() == 0 {
				t.Fatalf("check.sh without MONGO_TEST_URI: %v, want a non-zero exit:\n%s", err, out)
			}
			for _, want := range []string{"MONGO_TEST_URI", "make mongo"} {
				if !strings.Contains(string(out), want) {
					t.Errorf("the message does not mention %q:\n%s", want, out)
				}
			}
			if strings.Contains(string(out), "==>") {
				t.Errorf("check.sh started a check before refusing:\n%s", out)
			}
		})
	}
}

// --- the tests only touch their own databases -----------------------------------------------------------

// Every database a test drops is called test_<random hex>. Dropping happens only in the four
// helpers below; a new helper (or a test that drops a database itself) must be added here on
// purpose, after someone checked that it cannot name a developer's database such as `orch`.
// Running the suite against a shared or production server stays an operator risk: MONGO_TEST_URI
// decides which server gets the test_* databases.
func TestSecurityStartupTestsOnlyTouchRandomTestDatabases(t *testing.T) {
	allowed := map[string]bool{
		"../e2e/e2e_test.go":                                 true,
		"../internal/bootstrap/testdb_test.go":               true,
		"../internal/catalog/adapter/mongodb/testdb_test.go": true,
		"../internal/platform/mongodb/testdb_test.go":        true,
	}
	drop := regexp.MustCompile(`\.Drop\(|DropDatabase|dropDatabase`)
	literalDB := regexp.MustCompile(`\.Database\("([^"]*)"`)
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		slashed := filepath.ToSlash(path)
		if d.IsDir() {
			if slashed == "../internal/oas" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || filepath.Base(path) == "security_startup_test.go" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(raw)
		if drop.MatchString(src) {
			if !allowed[slashed] {
				t.Errorf("%s drops a database but is not one of the reviewed test-database helpers", slashed)
			} else if !strings.Contains(src, `"test_"`) || !strings.Contains(src, "crypto/rand") {
				t.Errorf("%s drops a database but does not build its name as \"test_\" + random hex", slashed)
			}
		}
		for _, m := range literalDB.FindAllStringSubmatch(src, -1) {
			if !strings.HasPrefix(m[1], "test_") {
				t.Errorf("%s opens the database %q by name; tests may only use test_<random hex> databases", slashed, m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	pattern := regexp.MustCompile(`^test_[0-9a-f]{16}$`)
	seen := map[string]bool{}
	for range 3 {
		name, _ := e2eTestDB(t)
		if !pattern.MatchString(name) {
			t.Errorf("test database name %q does not match test_<16 hex digits>", name)
		}
		if seen[name] {
			t.Errorf("test database name %q was handed out twice", name)
		}
		seen[name] = true
	}
}
