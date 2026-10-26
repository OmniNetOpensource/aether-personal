package httpapi

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/sandbox"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *server) userSandboxState(ctx context.Context, userID uuid.UUID) (string, string, error) {
	if s.sandboxClient == nil {
		return "disabled", "", nil
	}
	computer, err := s.queries.GetUserSandbox(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "unallocated", "", nil
	}
	if err != nil {
		return "", "", err
	}
	if computer.ProviderID == nil {
		return computer.ProvisionState, "", nil
	}
	environment, err := s.sandboxClient.Get(ctx, *computer.ProviderID)
	if errors.Is(err, sandbox.ErrNotFound) {
		return "missing", "", nil
	}
	if err != nil {
		return "", "", err
	}
	return environment.State, environment.ID, nil
}

func (s *server) sandboxStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	session := r.Context().Value(sessionContextKey{}).(database.GetActiveSessionRow)
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	state, _, err := s.userSandboxState(ctx, session.UserID)
	if err != nil {
		http.Error(w, "sandbox status unavailable", http.StatusBadGateway)
		return
	}
	writeAdminJSON(w, map[string]string{"state": state, "observed_at": time.Now().UTC().Format(time.RFC3339)})
}

func (s *server) sandboxDesktop(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	// Like chat, authentication is in the first frame; no cookies or URL credentials.
	connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer connection.CloseNow()
	connection.SetReadLimit(8 << 10)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	var auth struct {
		Type  string `json:"type"`
		Token string `json:"token"`
	}
	authContext, cancelAuth := context.WithTimeout(ctx, 5*time.Second)
	err = wsjson.Read(authContext, connection, &auth)
	cancelAuth()
	if err != nil || auth.Type != "auth" || len(auth.Token) == 0 || len(auth.Token) > 128 {
		connection.Close(websocket.StatusPolicyViolation, "unauthorized")
		return
	}
	session, err := s.findSession(ctx, auth.Token)
	if err != nil {
		connection.Close(websocket.StatusPolicyViolation, "unauthorized")
		return
	}
	if _, busy := s.desktopViewers.LoadOrStore(session.UserID, struct{}{}); busy {
		connection.Close(websocket.StatusTryAgainLater, "viewer already open")
		return
	}
	defer s.desktopViewers.Delete(session.UserID)
	stateContext, cancelState := context.WithTimeout(ctx, 8*time.Second)
	state, providerID, err := s.userSandboxState(stateContext, session.UserID)
	cancelState()
	if err != nil || state != "running" {
		connection.Close(websocket.StatusTryAgainLater, "desktop is not running")
		return
	}
	provider, err := s.sandboxClient.OpenDesktop(ctx, providerID)
	if err != nil {
		connection.Close(websocket.StatusTryAgainLater, "desktop unavailable")
		return
	}
	defer provider.Close()
	if err := wsjson.Write(ctx, connection, map[string]string{"type": "ready"}); err != nil {
		return
	}
	viewer := websocket.NetConn(ctx, connection, websocket.MessageBinary)
	defer viewer.Close()
	provider.SetDeadline(time.Now().Add(8 * time.Second))
	viewer.SetDeadline(time.Now().Add(8 * time.Second))
	if err := relayDesktopHandshake(provider, viewer); err != nil {
		return
	}
	provider.SetDeadline(time.Time{})
	viewer.SetDeadline(time.Time{})

	go func() {
		defer cancel()
		_ = relayDesktopRequests(provider, viewer)
	}()
	go func() {
		defer cancel()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		tokenHash := sha256.Sum256([]byte(auth.Token))
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				checkContext, cancelCheck := context.WithTimeout(ctx, 4*time.Second)
				_, sessionErr := s.queries.GetActiveSession(checkContext, tokenHash[:])
				state, currentID, stateErr := s.userSandboxState(checkContext, session.UserID)
				cancelCheck()
				if sessionErr != nil || stateErr != nil || state != "running" || currentID != providerID {
					return
				}
			}
		}
	}()
	_, _ = io.Copy(viewer, provider)
}
