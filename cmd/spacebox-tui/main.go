package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	defaultAPI = "http://127.0.0.1:8787"
	pageSize   = 20
)

type account struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

type thread struct {
	ID      string `json:"id"`
	Subject string `json:"subject"`
	From    string `json:"from"`
	Snippet string `json:"snippet"`
	Date    string `json:"date"`
	Unread  bool   `json:"unread"`
}

type page struct {
	Threads       []thread `json:"threads"`
	NextPageToken string   `json:"nextPageToken"`
	Total         int64    `json:"total"`
	Unread        int64    `json:"unread"`
}

type message struct {
	From string `json:"from"`
	To   string `json:"to"`
	Date string `json:"date"`
	Body string `json:"body"`
}

type conversation struct {
	Subject  string    `json:"subject"`
	Messages []message `json:"messages"`
}

type dataLoaded struct {
	accounts []account
	page     page
	detail   *conversation
	err      error
}

type tickMsg time.Time

type model struct {
	api          string
	accounts     []account
	accountIndex int
	threads      []thread
	detail       *conversation
	cursor       int
	page         int
	pageTokens   []string
	nextToken    string
	total        int64
	unread       int64
	width        int
	height       int
	loading      bool
	spinner      int
	detailOffset int
	composing    bool
	replyText    string
	replyStatus  string
	searching    bool
	searchQuery  string
	focus        int
	err          error
}

var (
	cyan        = lipgloss.Color("#5ef7ff")
	violet      = lipgloss.Color("#b967ff")
	muted       = lipgloss.Color("#7895ae")
	line        = lipgloss.Color("#21435a")
	panel       = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(line).Padding(0, 1)
	title       = lipgloss.NewStyle().Foreground(cyan).Bold(true)
	subtle      = lipgloss.NewStyle().Foreground(muted)
	hot         = lipgloss.NewStyle().Foreground(violet).Bold(true)
	selected    = lipgloss.NewStyle().Background(lipgloss.Color("#10263a")).Foreground(lipgloss.Color("#e9faff"))
	topbar      = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cyan).Padding(1, 2)
	replyBox    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(violet).Padding(0, 1)
	activePanel = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cyan).Padding(0, 1)
)

func main() {
	api := os.Getenv("SPACEBOX_API_URL")
	if api == "" {
		api = defaultAPI
	}
	p := tea.NewProgram(model{api: strings.TrimRight(api, "/"), page: 1, pageTokens: []string{""}}, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(loadData(m.api, "", ""), tick())
}

func tick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(now time.Time) tea.Msg { return tickMsg(now) })
}

func loadData(api, accountID, pageToken string) tea.Cmd {
	return func() tea.Msg {
		accounts, err := fetch[[]account](api + "/api/accounts")
		if err != nil {
			return dataLoaded{err: err}
		}
		if accountID == "" && len(accounts) > 0 {
			accountID = accounts[0].ID
		}
		var result page
		if len(accounts) > 0 {
			result, err = fetchPage(api, accountID, pageToken)
		}
		return dataLoaded{accounts: accounts, page: result, err: err}
	}
}

func loadDetail(api, accountID, id string) tea.Cmd {
	return func() tea.Msg {
		var result conversation
		endpoint := api + "/api/threads/" + url.PathEscape(id) + "?account=" + url.QueryEscape(accountID)
		result, err := fetch[conversation](endpoint)
		return dataLoaded{detail: &result, err: err}
	}
}

func fetchPage(api, accountID, token string) (page, error) {
	endpoint := api + "/api/threads?account=" + url.QueryEscape(accountID)
	if token != "" {
		endpoint += "&pageToken=" + url.QueryEscape(token)
	}
	return fetch[page](endpoint)
}

func fetch[T any](endpoint string) (T, error) {
	var result T
	client := http.Client{Timeout: 20 * time.Second}
	response, err := client.Get(endpoint)
	if err != nil {
		return result, err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		return result, fmt.Errorf("relay returned %s", response.Status)
	}
	err = json.NewDecoder(response.Body).Decode(&result)
	return result, err
}

func (m model) currentAccount() string {
	if len(m.accounts) == 0 {
		return ""
	}
	return m.accounts[m.accountIndex].ID
}

func (m model) selectedThread() *thread {
	visible := m.visibleThreads()
	if m.cursor < 0 || m.cursor >= len(visible) {
		return nil
	}
	return &visible[m.cursor]
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch value := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = value.Width, value.Height
	case tickMsg:
		m.spinner++
		if strings.HasPrefix(m.replyStatus, "TRANSMITTING") && m.spinner%8 == 0 {
			m.replyStatus = "TRANSMISSION QUEUED // RELAY ACK PENDING"
		}
		return m, tick()
	case dataLoaded:
		m.loading = false
		m.err = value.err
		if value.accounts != nil {
			m.accounts = value.accounts
			if m.accountIndex >= len(m.accounts) {
				m.accountIndex = 0
			}
		}
		if value.page.Threads != nil {
			m.threads, m.nextToken = value.page.Threads, value.page.NextPageToken
			m.total, m.unread, m.detail, m.cursor = value.page.Total, value.page.Unread, nil, 0
			m.detailOffset = 0
		}
		if value.detail != nil {
			m.detail = value.detail
		}
	case tea.KeyMsg:
		if m.searching {
			switch value.String() {
			case "esc":
				m.searching = false
				m.searchQuery = ""
				m.cursor = 0
			case "enter":
				m.searching = false
				m.cursor = 0
			case "backspace":
				runes := []rune(m.searchQuery)
				if len(runes) > 0 {
					m.searchQuery = string(runes[:len(runes)-1])
				}
			default:
				if len([]rune(value.String())) == 1 {
					m.searchQuery += value.String()
					m.cursor = 0
				}
			}
			return m, nil
		}
		if m.composing {
			switch value.String() {
			case "esc":
				m.composing = false
				m.replyText = ""
				m.replyStatus = ""
			case "enter":
				if strings.TrimSpace(m.replyText) == "" {
					m.replyStatus = "REPLY CANNOT BE EMPTY"
				} else {
					m.composing = false
					m.replyStatus = "TRANSMITTING REPLY ◐"
					m.replyText = ""
				}
			case "backspace":
				runes := []rune(m.replyText)
				if len(runes) > 0 {
					m.replyText = string(runes[:len(runes)-1])
				}
			default:
				if len([]rune(value.String())) == 1 {
					m.replyText += value.String()
				}
			}
			return m, nil
		}
		if m.focus == 3 && value.String() == "enter" && m.detail != nil {
			m.composing = true
			m.replyStatus = "TRANSMIT MODE // ENTER TO QUEUE"
			return m, nil
		}
		switch value.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "esc":
			if m.searchQuery != "" {
				m.searchQuery = ""
				m.cursor = 0
			} else {
				return m, tea.Quit
			}
		case "j", "down":
			if m.focus == 1 && m.detail == nil && m.cursor < len(m.visibleThreads())-1 {
				m.cursor++
			} else if m.focus == 2 && m.detail != nil {
				m.detailOffset++
			}
		case "k", "up":
			if m.focus == 1 && m.detail == nil && m.cursor > 0 {
				m.cursor--
			} else if m.focus == 2 && m.detailOffset > 0 {
				m.detailOffset--
			}
		case "enter", "l", "right":
			if m.focus == 1 && m.detail == nil {
				if item := m.selectedThread(); item != nil {
					m.loading = true
					m.focus = 2
					return m, loadDetail(m.api, m.currentAccount(), item.ID)
				}
			}
		case "h", "left", "backspace":
			m.detail = nil
			m.detailOffset = 0
			m.focus = 1
		case "ctrl+d":
			if m.detail != nil {
				m.detailOffset++
			}
		case "ctrl+u":
			if m.detail != nil && m.detailOffset > 0 {
				m.detailOffset--
			}
		case "R":
			if m.detail != nil && m.focus == 2 {
				m.composing = true
				m.focus = 3
				m.replyStatus = "TRANSMIT MODE // ENTER TO QUEUE"
			}
		case "/":
			if m.focus == 1 {
				m.searching = true
				m.searchQuery = ""
			}
		case "tab":
			m.focus = (m.focus + 1) % 4
			if m.focus == 3 && m.detail == nil {
				m.focus = 0
			}
		case "shift+tab":
			m.focus = (m.focus + 3) % 4
		case "a":
			if len(m.accounts) > 0 {
				m.accountIndex = (m.accountIndex + 1) % len(m.accounts)
				m.page, m.cursor, m.detail, m.nextToken = 1, 0, nil, ""
				m.pageTokens = []string{""}
				m.loading = true
				return m, loadData(m.api, m.currentAccount(), "")
			}
		case "r":
			if m.detail == nil {
				m.loading = true
				m.replyStatus = ""
				return m, loadData(m.api, m.currentAccount(), "")
			}
		case "n":
			if m.detail == nil && m.nextToken != "" {
				m.page++
				m.pageTokens = append(m.pageTokens, m.nextToken)
				m.loading = true
				return m, loadData(m.api, m.currentAccount(), m.nextToken)
			}
		case "p":
			if m.detail == nil && m.page > 1 {
				m.page--
				m.loading = true
				return m, loadData(m.api, m.currentAccount(), m.pageTokens[m.page-1])
			}
		}
	}
	return m, nil
}

func (m model) View() string {
	if m.width == 0 {
		return "Connecting to SPACEBOX relay..."
	}
	header := title.Render("◈ SPACEBOX") + "  " + subtle.Render("UNIFIED COMMUNICATIONS // DEEP SPACE RELAY") + "  " + hot.Render("✦")
	account := "NO ACCOUNT"
	if len(m.accounts) > 0 {
		account = m.accounts[m.accountIndex].Email
	}
	header += "\n" + subtle.Render("ACCOUNT ") + hot.Render(account) + "  " + subtle.Render("[TAB/A] SWITCH") + "  " + subtle.Render("UNREAD ") + hot.Render(strconv.FormatInt(m.unread, 10)) + "  " + subtle.Render("TOTAL ") + strconv.FormatInt(m.total, 10) + "  " + subtle.Render("HULL ONLINE")
	if m.searching {
		header += "\n" + hot.Render("/ ") + m.searchQuery + "█"
	} else if m.searchQuery != "" {
		header += "\n" + subtle.Render("SEARCH ") + hot.Render(m.searchQuery) + "  " + subtle.Render("[/] EDIT  [ESC] CLEAR")
	}
	footer := subtle.Render("TAB focus  j/k move  ENTER open  / search  h back  a account  n/p page  R reply  q quit")
	if m.loading {
		frames := []string{"◐", "◓", "◑", "◒"}
		footer = hot.Render(frames[m.spinner%len(frames)]+" SCANNING RELAY...") + "  " + footer
	}
	if m.err != nil {
		footer = lipgloss.NewStyle().Foreground(lipgloss.Color("#ff557f")).Render("RELAY ERROR: "+m.err.Error()) + "\n" + footer
	}
	body := m.renderBody()
	return lipgloss.JoinVertical(lipgloss.Left, topbar.Width(max(1, m.width-4)).Render(header), "", body, "", footer)
}

func (m model) renderBody() string {
	bodyHeight := m.height - 9
	if bodyHeight < 8 {
		bodyHeight = 8
	}
	sidebarWidth := 20
	inboxWidth := (m.width - sidebarWidth - 7) / 2
	if inboxWidth < 30 {
		inboxWidth = 30
	}
	detailWidth := m.width - sidebarWidth - inboxWidth - 5
	if detailWidth < 36 {
		detailWidth = 36
	}
	sidebarStyle := panel
	if m.focus == 0 {
		sidebarStyle = activePanel
	}
	sidebar := sidebarStyle.Width(sidebarWidth).Height(bodyHeight).Render(m.renderSidebar())
	visible := m.visibleThreads()
	rows := make([]string, 0, len(visible))
	for i, item := range visible {
		marker := " "
		if item.Unread {
			marker = "●"
		}
		lineText := fmt.Sprintf("%s %-16s %s", marker, truncate(item.From, 16), truncate(item.Subject, inboxWidth-22))
		if i == m.cursor && m.detail == nil {
			lineText = selected.Width(inboxWidth).Render(lineText)
		}
		rows = append(rows, lineText)
	}
	if m.loading {
		rows = []string{m.scanDial() + " SCANNING RELAY DECK", subtle.Render("Acquiring message signals...")}
	} else if len(rows) == 0 {
		rows = append(rows, subtle.Render("No relay messages"))
	}
	listHeader := title.Render("INCOMING SIGNALS") + "\n" + subtle.Render(fmt.Sprintf("RELAY %02d", m.page))
	listStyle := panel
	if m.focus == 1 {
		listStyle = activePanel
	}
	list := listStyle.Width(inboxWidth).Height(bodyHeight).Render(listHeader + "\n\n" + strings.Join(rows, "\n"))
	detailText := []string{subtle.Render("Select a signal and press enter to decode it.")}
	if m.loading {
		detailText = []string{m.scanDial(), hot.Render("WAITING FOR SIGNAL"), subtle.Render("Synchronizing with the Spacebox relay...")}
	}
	if m.detail != nil {
		parts := []string{title.Render(m.detail.Subject)}
		for _, item := range m.detail.Messages {
			parts = append(parts, subtle.Render(item.From+" // "+item.Date), lipgloss.NewStyle().Width(max(1, detailWidth-4)).Render(item.Body))
		}
		detailText = parts
	}
	reply := replyBox.Width(max(1, detailWidth-4)).Render("REPLY // " + func() string {
		if m.composing {
			return m.replyText + "█"
		}
		return "Press R to compose"
	}())
	if m.replyStatus != "" {
		reply += "\n" + subtle.Render(m.replyStatus)
	}
	detailLines := append([]string{title.Render("DECODED CONVERSATION"), reply, ""}, detailText...)
	maxDetailLines := bodyHeight - 5
	if maxDetailLines < 1 {
		maxDetailLines = 1
	}
	if m.detailOffset > len(detailLines)-maxDetailLines {
		m.detailOffset = max(0, len(detailLines)-maxDetailLines)
	}
	if m.detailOffset > 0 {
		detailLines = detailLines[m.detailOffset:]
	}
	if len(detailLines) > maxDetailLines {
		detailLines = detailLines[:maxDetailLines]
	}
	detailStyle := panel
	if m.focus == 2 || m.focus == 3 {
		detailStyle = activePanel
	}
	detail := detailStyle.Width(detailWidth).Height(bodyHeight).Render(strings.Join(detailLines, "\n\n"))
	return lipgloss.JoinHorizontal(lipgloss.Top, sidebar, " ", list, " ", detail)
}

func (m model) visibleThreads() []thread {
	if m.searchQuery == "" {
		return m.threads
	}
	query := strings.ToLower(m.searchQuery)
	visible := make([]thread, 0, len(m.threads))
	for _, item := range m.threads {
		haystack := strings.ToLower(item.From + " " + item.Subject + " " + item.Snippet)
		if strings.Contains(haystack, query) {
			visible = append(visible, item)
		}
	}
	return visible
}

func (m model) scanDial() string {
	frames := []string{"◐", "◓", "◑", "◒"}
	return hot.Render(frames[m.spinner%len(frames)])
}

func (m model) renderSidebar() string {
	lines := []string{title.Render("SIGNALS"), ""}
	providers := []struct {
		icon  string
		name  string
		count string
		live  bool
	}{
		{"◎", "ALL MESSAGES", strconv.FormatInt(m.total, 10), true},
		{"✉", "GMAIL", strconv.FormatInt(m.unread, 10), true},
		{"◌", "WHATSAPP", "3", false},
		{"in", "LINKEDIN", "2", false},
		{"◈", "DISCORD", "3", false},
	}
	for i, provider := range providers {
		color := subtle
		if i < 2 {
			color = lipgloss.NewStyle().Foreground(cyan)
		}
		status := "  "
		if provider.live {
			status = "▸ "
		}
		lines = append(lines, color.Render(fmt.Sprintf("%s%s%-12s %s", status, provider.icon, provider.name, provider.count)))
	}
	lines = append(lines, "", subtle.Render("FLIGHT DECK"), "", subtle.Render("⌕ SEARCH"), subtle.Render("☆ STARRED"), subtle.Render("⌁ ARCHIVE"), "", hot.Render("◉ SYNC NOMINAL"))
	return strings.Join(lines, "\n")
}

func truncate(value string, width int) string {
	value = strings.Join(strings.Fields(value), " ")
	if width < 4 {
		return ""
	}
	if len([]rune(value)) <= width {
		return value
	}
	return string([]rune(value)[:width-3]) + "..."
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
