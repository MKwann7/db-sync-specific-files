package auth

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// LoginResult is the outcome of an interactive authorization-code exchange.
type LoginResult struct {
	Tokens TokenSet
}

// callbackTimeout bounds how long the local HTTP listener waits for the
// user to complete the browser-based authorization step.
const callbackTimeout = 5 * time.Minute

// RunLocalCallbackLogin starts a temporary, loopback-only HTTP listener on
// port, prints the authorization URL for the user to open, waits for the
// OAuth redirect carrying the authorization code, validates the CSRF state,
// and exchanges the code for tokens.
//
// The listener binds only to 127.0.0.1 and is torn down as soon as the
// single callback request is handled or the timeout elapses.
func RunLocalCallbackLogin(ctx context.Context, creds Credentials, port int, printURL func(url string)) (LoginResult, error) {
	state, err := GenerateState()
	if err != nil {
		return LoginResult{}, err
	}

	redirectURI := fmt.Sprintf("http://localhost:%d/callback", port)
	authURL := AuthorizationURL(creds, state, redirectURI)

	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return LoginResult{}, fmt.Errorf("starting local oauth callback listener on port %d: %w", port, err)
	}
	defer listener.Close()

	type callbackResult struct {
		code string
		err  error
	}
	resultCh := make(chan callbackResult, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if errParam := q.Get("error"); errParam != "" {
			resultCh <- callbackResult{err: fmt.Errorf("dropbox authorization denied or failed: %s", errParam)}
			http.Error(w, "Authorization failed. You may close this window.", http.StatusBadRequest)
			return
		}
		gotState := q.Get("state")
		if gotState == "" || gotState != state {
			resultCh <- callbackResult{err: fmt.Errorf("oauth state mismatch: possible CSRF attempt")}
			http.Error(w, "Invalid state parameter. You may close this window.", http.StatusBadRequest)
			return
		}
		code := q.Get("code")
		if code == "" {
			resultCh <- callbackResult{err: fmt.Errorf("no authorization code returned by dropbox")}
			http.Error(w, "Missing authorization code. You may close this window.", http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, "Authorization successful. You may close this window and return to the terminal.")
		resultCh <- callbackResult{code: code}
	})

	srv := &http.Server{Handler: mux}
	go srv.Serve(listener)
	defer srv.Close()

	printURL(authURL)

	select {
	case res := <-resultCh:
		if res.err != nil {
			return LoginResult{}, res.err
		}
		tokens, err := ExchangeCode(ctx, creds, res.code, redirectURI)
		if err != nil {
			return LoginResult{}, fmt.Errorf("exchanging authorization code: %w", err)
		}
		return LoginResult{Tokens: tokens}, nil
	case <-time.After(callbackTimeout):
		return LoginResult{}, fmt.Errorf("timed out waiting for dropbox authorization callback after %s", callbackTimeout)
	case <-ctx.Done():
		return LoginResult{}, ctx.Err()
	}
}

// ManualExchange supports the documented manual-authorization-code fallback
// for environments without a reachable loopback browser (e.g. remote
// servers): the user opens the authorization URL themselves (generated with
// an empty redirect URI, which causes Dropbox to display the code directly
// in the browser instead of redirecting), copies the resulting code, and
// this function performs the token exchange.
func ManualExchange(ctx context.Context, creds Credentials, code string) (TokenSet, error) {
	return ExchangeCode(ctx, creds, code, "")
}
