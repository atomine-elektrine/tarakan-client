package app

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"tarakan-client/internal/agent"
	"tarakan-client/internal/api"
	repoctx "tarakan-client/internal/context"
	"tarakan-client/internal/session"
)

const (
	minimumWidth  = 48
	minimumHeight = 14
)

var (
	accent      = lipgloss.Color("#E05A33")
	muted       = lipgloss.Color("#777777")
	subtle      = lipgloss.Color("#353535")
	brandStyle  = lipgloss.NewStyle().Bold(true).Foreground(accent)
	mutedStyle  = lipgloss.NewStyle().Foreground(muted)
	systemStyle = lipgloss.NewStyle().Foreground(muted)
	userStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#F2F2F2"))
	agentStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#D7D7D7"))
	errorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF6B6B"))
	headerStyle = lipgloss.NewStyle().Padding(0, 1).BorderBottom(true).BorderStyle(lipgloss.NormalBorder()).BorderForeground(subtle)
	inputStyle  = lipgloss.NewStyle().Padding(0, 1).BorderTop(true).BorderStyle(lipgloss.NormalBorder()).BorderForeground(subtle)
	footerStyle = lipgloss.NewStyle().Padding(0, 1).Foreground(muted)
)

type Model struct {
	repository repoctx.Info
	registry   agent.Registry
	selected   agent.Provider
	apiConfig  api.Config
	transcript session.Transcript
	viewport   viewport.Model
	input      textarea.Model
	width      int
	height     int
	busy       bool
	// busyStatus is the live footer/status line while busy (clone, agent, …).
	busyStatus string
	// workEvents receives live progress from a background job; nil when idle.
	workEvents <-chan workEvent

	// startJobID, when set (CLI --job), auto-starts that job after mount.
	// startPickup, when set (CLI --pickup / report --interactive without --job),
	// claims the next open report job for this repository.
	startJobID  int64
	startPickup bool

	// Pending, human-reviewable artifacts awaiting an explicit submit command.
	pendingEvidence  *pendingEvidence
	pendingScan      *pendingScan
	pendingJobReport *pendingJobReport
	pendingVerdict   *pendingVerdict
	pendingLogin     *pendingLogin
}

// SessionOpts configures auto-start behavior for the interactive UI.
type SessionOpts struct {
	JobID     int64      // claim+run this job on start (0 = none)
	Pickup    bool       // claim+run the next open report job on start
	APIConfig api.Config // host URL + token (--url/--token or env)
}

func New(repository repoctx.Info, registry agent.Registry, selected agent.Provider) Model {
	return NewSession(repository, registry, selected, SessionOpts{})
}

// NewWithJob builds the interactive session and optionally auto-starts a job.
func NewWithJob(repository repoctx.Info, registry agent.Registry, selected agent.Provider, jobID int64) Model {
	return NewSession(repository, registry, selected, SessionOpts{JobID: jobID})
}

// NewSession builds the interactive session.
func NewSession(repository repoctx.Info, registry agent.Registry, selected agent.Provider, opts SessionOpts) Model {
	input := textarea.New()
	input.Placeholder = "Next: /login"
	input.Prompt = "› "
	input.ShowLineNumbers = false
	input.DynamicHeight = true
	input.MinHeight = 1
	input.MaxHeight = 3
	input.MaxContentHeight = 6
	input.CharLimit = 4_000
	input.SetVirtualCursor(true)
	// Focus must be set on this value before it is stored. Init() receives a
	// copy of the Model, so m.input.Focus() there would only focus a throwaway
	// textarea and leave the real one ignoring every keypress.
	_ = input.Focus()

	view := viewport.New()
	view.SoftWrap = true
	view.FillHeight = true

	// Explicit job wins over free-form pickup.
	pickup := opts.Pickup && opts.JobID <= 0

	apiConfig := opts.APIConfig
	if apiConfig.BaseURL == "" && apiConfig.Token == "" {
		apiConfig = api.LoadConfig("", "")
	}

	model := Model{
		repository:  repository,
		registry:    registry,
		selected:    selected,
		apiConfig:   apiConfig,
		viewport:    view,
		input:       input,
		width:       80,
		height:      24,
		startJobID:  opts.JobID,
		startPickup: pickup,
	}
	model.transcript.Append(session.RoleSystem, startupContextLine(repository))
	model.transcript.Append(session.RoleSystem, "API "+apiConfig.Summary()+"  (/login to sign in; /config to inspect)")
	model.appendDetectionStatus()
	switch {
	case opts.JobID > 0:
		model.transcript.Append(session.RoleSystem, fmt.Sprintf(
			"Starting job #%d: claim, run agent, then /submit-report when you accept the findings.", opts.JobID))
	case pickup:
		model.transcript.Append(session.RoleSystem,
			"Auto-pickup: will claim the next open report job from the global queue (preferring this repo), run the agent, then wait for /submit-report.")
	default:
		model.appendWorkflowGuide()
	}
	model.updateInputHint()
	model.resize(model.width, model.height)
	model.refreshTranscript()
	return model
}

func (m Model) Init() tea.Cmd {
	// Cursor blink (and re-assert focus). Focus on the stored model was set in New.
	cmds := []tea.Cmd{m.input.Focus(), m.viewport.Init()}
	switch {
	case m.startJobID > 0:
		id := m.startJobID
		cmds = append(cmds, func() tea.Msg { return startJobMsg{id: id} })
	case m.startPickup:
		cmds = append(cmds, func() tea.Msg { return startPickupMsg{} })
	}
	return tea.Batch(cmds...)
}

func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.resize(message.Width, message.Height)
		m.refreshTranscript()
		return m, nil
	case tea.KeyPressMsg:
		switch message.String() {
		case "ctrl+c":
			return m, quit
		case "enter":
			if !m.busy {
				return m.submit()
			}
		}
	case workEventMsg:
		return m.handleWorkEvent(message)
	case startJobMsg:
		return m.beginReportJob(message.id)
	case startPickupMsg:
		return m.beginPickup()
	case loginStartedMsg:
		return m.handleLoginStarted(message)
	case loginPollMsg:
		return m.handleLoginPoll(message)
	case loginPollTickMsg:
		if m.pendingLogin == nil {
			return m, nil
		}
		return m, pollLogin(m.pendingLogin)
	case pickedJobMsg, noticeMsg, evidenceReadyMsg, reviewReadyMsg, jobReportReadyMsg, verdictReadyMsg:
		return m.handleWorkMessage(message)
	}

	var commands []tea.Cmd
	var command tea.Cmd
	m.viewport, command = m.viewport.Update(message)
	commands = append(commands, command)
	m.input, command = m.input.Update(message)
	commands = append(commands, command)
	m.resize(m.width, m.height)
	return m, tea.Batch(commands...)
}

func (m Model) View() tea.View {
	content := lipgloss.JoinVertical(
		lipgloss.Left,
		m.renderHeader(),
		m.viewport.View(),
		inputStyle.Width(m.width-3).Render(m.input.View()),
		m.renderFooter(),
	)
	view := tea.NewView(content)
	view.AltScreen = true
	view.WindowTitle = "Tarakan - " + m.repository.Name
	return view
}

func (m Model) submit() (tea.Model, tea.Cmd) {
	value := strings.TrimSpace(m.input.Value())
	if value == "" {
		return m, nil
	}
	m.input.Reset()

	if command, ok := parseCommand(value); ok {
		return m.executeCommand(command)
	}

	next := "/pickup to claim the next report job"
	if m.apiConfig.Token == "" {
		next = "/login to sign in"
	}
	m.transcript.Append(session.RoleSystem,
		"Tarakan uses a guided review workflow; ordinary text does not run an agent. Use "+next+". Use /review only when you intentionally want to review the current repository.")
	m.updateInputHint()
	m.refreshTranscript()
	return m, nil
}

func (m Model) executeCommand(command command) (tea.Model, tea.Cmd) {
	switch command.name {
	case "help":
		m.transcript.Append(session.RoleSystem, strings.Join([]string{
			"API",
			"  /login               sign in through the Tarakan web app",
			"  /url <host>          set Tarakan base URL (default https://tarakan.lol)",
			"  /token <secret>      set API token (shown masked only)",
			"  /config              show current url + masked token",
			"Backend",
			"  /agent [name]        list backends, or choose claude|codex|grok|ollama|openrouter",
			"  /model <name>        set the model for an HTTP backend (ollama, openrouter)",
			"  /context             show repository context",
			"Jobs (preferred)",
			"  /jobs                open jobs for this repository",
			"  /pickup              next open report job from the global queue + run agent",
			"  /report              same as /pickup (prefers jobs for this repo when present)",
			"  /report <id>         claim that job, run agent (Review Format)",
			"  /submit-report       publish pending job Report (Findings on the repo)",
			"  /task <id>           show a job",
			"  /claim <id>          claim only   ·   /release <id>  release",
			"  /run <id>            agent prose evidence (legacy) · /submit <id> <summary>",
			"Reviews & verification",
			"  /queue               repositories awaiting review",
			"  /scans               reviews of this repository (findings if authorized)",
			"  /review              ad-hoc agent review of this repo → pending scan",
			"  /submit-review       submit the pending ad-hoc review",
			"  /verify <scan id>    run your agent to verify a review → pending verdict",
			"  /submit-verdict      submit the pending verdict + proof of concept",
			"Session",
			"  /clear               clear transcript   ·   /quit  exit",
		}, "\n"))
	case "login":
		return m.beginLogin()
	case "url":
		if len(command.args) == 0 {
			m.transcript.Append(session.RoleSystem, "Usage: /url https://tarakan.lol   (current: "+m.apiConfig.BaseURL+")")
			break
		}
		candidate := m.apiConfig.WithOverrides(command.args[0], "")
		// Validate URL even when token is not set yet.
		checkToken := candidate.Token
		if checkToken == "" {
			checkToken = "placeholder-for-url-check"
		}
		if _, err := api.New(candidate.BaseURL, checkToken, nil); err != nil {
			m.transcript.Append(session.RoleSystem, "Invalid URL: "+err.Error())
			break
		}
		m.apiConfig = candidate
		m.transcript.Append(session.RoleSystem, "API url set to "+m.apiConfig.BaseURL)
	case "token":
		if len(command.args) == 0 {
			m.transcript.Append(session.RoleSystem, "Usage: /token <api-token>   (current: "+m.apiConfig.MaskedToken()+")")
			break
		}
		m.apiConfig = m.apiConfig.WithOverrides("", strings.Join(command.args, " "))
		m.transcript.Append(session.RoleSystem, "API token set ("+m.apiConfig.MaskedToken()+").")
	case "config":
		m.transcript.Append(session.RoleSystem, "API "+m.apiConfig.Summary())
	case "agent":
		if len(command.args) == 0 {
			providers := m.registry.Providers()
			if len(providers) == 0 {
				m.transcript.Append(session.RoleSystem, "No review backends detected.")
				break
			}
			names := make([]string, 0, len(providers))
			for _, provider := range providers {
				label := provider.Name
				if provider.Kind == agent.KindHTTP && provider.Model != "" {
					label += " (" + provider.Model + ")"
				}
				if provider.Name == m.selected.Name {
					label += " (selected)"
				}
				names = append(names, label)
			}
			m.transcript.Append(session.RoleSystem, "Available backends: "+strings.Join(names, ", "))
			break
		}
		provider, ok := m.registry.Find(command.args[0])
		if !ok {
			m.transcript.Append(session.RoleSystem, fmt.Sprintf("Backend %q is not installed or configured.", command.args[0]))
			break
		}
		m.selected = provider
		m.transcript.Append(session.RoleSystem, provider.Description+" selected.")
	case "model":
		if len(command.args) == 0 {
			m.transcript.Append(session.RoleSystem, "Usage: /model <name> (applies to ollama or openrouter)")
			break
		}
		if m.selected.Kind != agent.KindHTTP {
			m.transcript.Append(session.RoleSystem, "The selected backend uses its own model; /model applies to ollama or openrouter.")
			break
		}
		m.selected = m.selected.WithModel(command.args[0])
		m.transcript.Append(session.RoleSystem, m.selected.Description+" model set to "+m.selected.Model+".")
	case "context":
		context := fmt.Sprintf("Repository: %s\nRoot: %s", m.repository.Name, m.repository.Root)
		if m.repository.IsGit {
			context += fmt.Sprintf("\nBranch: %s\nCommit: %s", valueOr(m.repository.Branch, "detached"), valueOr(m.repository.Commit, "unborn"))
		}
		m.transcript.Append(session.RoleSystem, context)
	case "clear":
		m.transcript.Clear()
		m.transcript.Append(session.RoleSystem, "Transcript cleared.")
	case "quit", "exit":
		return m, quit
	case "jobs", "task", "claim", "release", "report", "pickup", "submit-report", "run", "submit",
		"queue", "scans", "review", "submit-review", "verify", "submit-verdict":
		return m.executeWorkCommand(command)
	default:
		m.transcript.Append(session.RoleSystem, fmt.Sprintf("Unknown command /%s. Type /help.", command.name))
	}
	m.refreshTranscript()
	return m, nil
}

func (m *Model) appendDetectionStatus() {
	providers := m.registry.Providers()
	if len(providers) == 0 {
		m.transcript.Append(session.RoleSystem, "No review backend detected.")
		return
	}
	names := make([]string, 0, len(providers))
	for _, provider := range providers {
		names = append(names, provider.Name)
	}
	status := "Detected: " + strings.Join(names, ", ") + "."
	if m.selected.Name != "" {
		status += " Using " + m.selected.Name + "."
	}
	m.transcript.Append(session.RoleSystem, status)
}

func (m *Model) appendWorkflowGuide() {
	if m.apiConfig.Token == "" {
		m.transcript.Append(session.RoleSystem, strings.Join([]string{
			"Workflow",
			"  1. /login     sign in through tarakan.lol  ← next",
			"  2. /pickup    claim a public review job and run the selected agent",
			"  3. inspect    review the structured findings",
			"  4. /submit-report  publish only when you approve the result",
		}, "\n"))
		return
	}
	m.transcript.Append(session.RoleSystem, strings.Join([]string{
		"Workflow",
		"  1. signed in  ✓",
		"  2. /pickup    claim a public review job and run the selected agent  ← next",
		"  3. inspect    review the structured findings",
		"  4. /submit-report  publish only when you approve the result",
	}, "\n"))
}

func (m *Model) updateInputHint() {
	switch {
	case m.apiConfig.Token == "":
		m.input.Placeholder = "Next: /login"
	case m.pendingJobReport != nil:
		m.input.Placeholder = "Next: /submit-report (after reviewing findings)"
	case m.pendingVerdict != nil:
		m.input.Placeholder = "Next: /submit-verdict (after reviewing checks)"
	case m.pendingScan != nil:
		m.input.Placeholder = "Next: /submit-review (after reviewing findings)"
	default:
		m.input.Placeholder = "Next: /pickup  ·  /help for other actions"
	}
}

func (m *Model) resize(width, height int) {
	m.width = max(width, minimumWidth)
	m.height = max(height, minimumHeight)
	m.input.SetWidth(m.width - 4)
	headerHeight := lipgloss.Height(m.renderHeader())
	inputHeight := lipgloss.Height(inputStyle.Width(m.width - 3).Render(m.input.View()))
	footerHeight := lipgloss.Height(m.renderFooter())
	m.viewport.SetWidth(m.width)
	m.viewport.SetHeight(max(3, m.height-headerHeight-inputHeight-footerHeight))
}

func (m *Model) refreshTranscript() {
	wasAtBottom := m.viewport.AtBottom()
	var builder strings.Builder
	for index, message := range m.transcript.Messages() {
		if index > 0 {
			builder.WriteString("\n\n")
		}
		label := string(message.Role)
		style := agentStyle
		switch message.Role {
		case session.RoleSystem:
			style = systemStyle
		case session.RoleUser:
			style = userStyle
		}
		if strings.HasPrefix(message.Content, "Error:") {
			style = errorStyle
		}
		builder.WriteString(style.Render(label + "\n" + message.Content))
	}
	m.viewport.SetContent(lipgloss.NewStyle().Padding(1, 2).Width(max(1, m.width-4)).Render(builder.String()))
	if wasAtBottom || m.viewport.TotalLineCount() <= m.viewport.Height() {
		m.viewport.GotoBottom()
	}
}

func (m Model) renderHeader() string {
	repository := m.repository.Name
	if m.repository.IsGit {
		repository += "  " + valueOr(m.repository.Branch, "detached")
		if m.repository.Commit != "" {
			repository += "@" + m.repository.Commit
		}
	}
	agentName := "no agent"
	if m.selected.Name != "" {
		agentName = m.selected.Name
	}
	left := brandStyle.Render("TARAKAN") + "  " + mutedStyle.Render(repository)
	right := mutedStyle.Render(agentName)
	space := strings.Repeat(" ", max(1, m.width-lipgloss.Width(left)-lipgloss.Width(right)-3))
	return headerStyle.Width(m.width - 1).Render(left + space + right)
}

func (m Model) renderFooter() string {
	status := "enter run command  ·  /help commands  ·  ctrl+c quit"
	if m.busy {
		if m.busyStatus != "" {
			status = truncateRunes(m.busyStatus, max(20, m.width-18)) + "  ·  ctrl+c quits"
		} else if m.selected.Name != "" {
			status = m.selected.Description + " is working  ·  ctrl+c quits"
		} else {
			status = "Working…  ·  ctrl+c quits"
		}
	}
	return footerStyle.Width(m.width - 3).Render(status)
}

func truncateRunes(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen <= 1 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-1]) + "…"
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// startupContextLine explains what directory Tarakan is attached to. This is
// only local cwd discovery - not "you already claimed a job" or "API is ready".
func startupContextLine(repository repoctx.Info) string {
	if !repository.IsGit {
		return "Working directory: " + repository.Root + " (not a git repo). /pickup can still clone a job elsewhere."
	}
	line := "Local git: " + repository.Root
	if owner, name, ok := repository.RemoteSlug(); ok {
		if repository.Host != "" {
			line += " · origin " + repository.Host + "/" + owner + "/" + name
		} else {
			line += " · origin " + owner + "/" + name
		}
	} else {
		line += " · no origin remote"
	}
	if repository.Branch != "" || repository.Commit != "" {
		line += " · " + valueOr(repository.Branch, "detached")
		if repository.Commit != "" {
			line += "@" + repository.Commit
		}
	}
	return line
}

func quit() tea.Msg {
	return tea.Quit()
}
