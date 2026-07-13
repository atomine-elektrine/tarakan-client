package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"tarakan-client/internal/api"
	"tarakan-client/internal/browser"
	"tarakan-client/internal/session"
)

type pendingLogin struct {
	config        api.Config
	authorization api.DeviceAuthorization
	expiresAt     time.Time
}

type loginStartedMsg struct {
	authorization api.DeviceAuthorization
	err           error
}

type loginPollMsg struct {
	credential api.DeviceCredential
	err        error
}

type loginPollTickMsg struct{}

func (m Model) beginLogin() (tea.Model, tea.Cmd) {
	m.busy = true
	m.busyStatus = "Starting browser login…"
	m.transcript.Append(session.RoleSystem, "Starting browser login at "+m.apiConfig.BaseURL+"…")
	m.refreshTranscript()
	m.resize(m.width, m.height)
	config := m.apiConfig
	return m, func() tea.Msg {
		client, err := api.NewPublic(config.BaseURL, nil)
		if err != nil {
			return loginStartedMsg{err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		authorization, err := client.StartDeviceAuthorization(ctx, tuiClientName())
		return loginStartedMsg{authorization: authorization, err: err}
	}
}

func (m Model) handleLoginStarted(message loginStartedMsg) (tea.Model, tea.Cmd) {
	if message.err != nil {
		return m.finishLoginError(fmt.Errorf("start web login: %w", message.err))
	}
	authorization := message.authorization
	m.pendingLogin = &pendingLogin{
		config:        m.apiConfig,
		authorization: authorization,
		expiresAt:     time.Now().Add(time.Duration(authorization.ExpiresIn) * time.Second),
	}
	m.busyStatus = "Waiting for browser approval…"
	m.transcript.Append(
		session.RoleSystem,
		"Confirm code "+authorization.UserCode+" in your browser:\n"+authorization.VerificationURIComplete,
	)
	if err := browser.Open(authorization.VerificationURIComplete); err != nil {
		m.transcript.Append(session.RoleSystem, "Could not open a browser automatically: "+err.Error()+"\nOpen the URL above to continue.")
	}
	m.refreshTranscript()
	m.resize(m.width, m.height)
	return m, pollLogin(m.pendingLogin)
}

func (m Model) handleLoginPoll(message loginPollMsg) (tea.Model, tea.Cmd) {
	if m.pendingLogin == nil {
		return m, nil
	}
	switch {
	case message.err == nil && strings.TrimSpace(message.credential.Token) != "":
		config := m.pendingLogin.config.WithOverrides("", message.credential.Token)
		path, err := api.SaveConfig(config)
		if err != nil {
			return m.finishLoginError(fmt.Errorf("save login: %w", err))
		}
		m.apiConfig = config
		m.pendingLogin = nil
		m.busy = false
		m.busyStatus = ""
		m.transcript.Append(session.RoleSystem, "Logged in to "+config.BaseURL+". Credentials saved to "+path+".\n\nNext: /pickup to claim a review job.")
		m.updateInputHint()
		m.refreshTranscript()
		m.resize(m.width, m.height)
		return m, nil
	case errors.Is(message.err, api.ErrAuthorizationPending):
		if time.Now().After(m.pendingLogin.expiresAt) {
			return m.finishLoginError(errors.New("web login expired; run /login to try again"))
		}
		interval := time.Duration(m.pendingLogin.authorization.Interval) * time.Second
		if interval < time.Second {
			interval = 2 * time.Second
		}
		return m, tea.Tick(interval, func(time.Time) tea.Msg { return loginPollTickMsg{} })
	case errors.Is(message.err, api.ErrAccessDenied):
		return m.finishLoginError(errors.New("web login was denied"))
	case errors.Is(message.err, api.ErrDeviceCodeExpired):
		return m.finishLoginError(errors.New("web login expired; run /login to try again"))
	case message.err == nil:
		return m.finishLoginError(errors.New("server returned an empty credential"))
	default:
		return m.finishLoginError(fmt.Errorf("finish web login: %w", message.err))
	}
}

func (m Model) finishLoginError(err error) (tea.Model, tea.Cmd) {
	m.pendingLogin = nil
	m.busy = false
	m.busyStatus = ""
	m.transcript.Append(session.RoleSystem, "Login error: "+err.Error())
	m.updateInputHint()
	m.refreshTranscript()
	m.resize(m.width, m.height)
	return m, nil
}

func pollLogin(login *pendingLogin) tea.Cmd {
	return func() tea.Msg {
		client, err := api.NewPublic(login.config.BaseURL, nil)
		if err != nil {
			return loginPollMsg{err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		credential, err := client.ExchangeDeviceAuthorization(ctx, login.authorization.DeviceCode)
		return loginPollMsg{credential: credential, err: err}
	}
}

func tuiClientName() string {
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		return "Tarakan TUI"
	}
	return "Tarakan TUI on " + hostname
}
