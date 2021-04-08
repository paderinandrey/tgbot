package main

import (
	"context"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	tgbotapi "gopkg.in/telegram-bot-api.v4"
)

var (
	BotToken   = "1720733611:AAFCNZUkTIiDB-ZN5jHZlTr2ZCW6sOqY1g4"
	WebhookURL = "https://tasks-golang-course-bot.herokuapp.com"
	// WebhookURL = "https://tasks-golang-course-bot.herokuapp.com"
)

type User struct {
	ID       int64
	UserName string
}

type Task struct {
	ID       int64
	Title    string
	Owner    *User
	Assignee *User
	Resolve  bool
}

func (t *Task) Assign(assegnee *User) {
	t.Assignee = assegnee
}

func (t *Task) UnassignTask() {
	t.Assignee = nil
}

func (t *Task) ResolveTask() {
	t.Resolve = true
}

type TasksRepo struct {
	maxID int64
	tasks []*Task
	mu    *sync.RWMutex
}

func newTasksRepo() *TasksRepo {
	return &TasksRepo{
		maxID: 1,
		tasks: make([]*Task, 0, 10),
		mu:    &sync.RWMutex{},
	}
}

func (t *TasksRepo) ByUser(user *User) []*Task {
	result := make([]*Task, 0)
	result = append(result, t.ByAssignee(user)...)
	result = append(result, t.ByOwner(user)...)
	return result
}

func (t *TasksRepo) ByAssignee(user *User) []*Task {
	result := make([]*Task, 0)
	for _, task := range t.tasks {
		if task.Resolve {
			continue
		}
		if task.Assignee != nil && task.Assignee.ID == user.ID {
			result = append(result, task)
		}
	}
	return result
}

func (t *TasksRepo) ByOwner(user *User) []*Task {
	result := make([]*Task, 0)
	for _, task := range t.tasks {
		if task.Resolve {
			continue
		}
		if task.Owner.ID == user.ID {
			result = append(result, task)
		}
	}
	return result
}

func (t *TasksRepo) UnresolvedTasks() []*Task {
	result := make([]*Task, 0)
	for _, task := range t.tasks {
		if task.Resolve {
			continue
		}

		result = append(result, task)

	}
	return result
}

func (t *TasksRepo) Add(title string, owner *User) *Task {
	task := &Task{
		ID:    t.maxID,
		Title: title,
		Owner: owner,
	}

	t.mu.Lock()
	t.maxID++
	t.tasks = append(t.tasks, task)
	t.mu.Unlock()
	return task
}

func (t *TasksRepo) Find(id int64) (*Task, bool) {
	for _, task := range t.tasks {
		if task.ID == id {
			return task, true
		}
	}
	return nil, false
}

const tasksTmpl = `
{{ range .Tasks }}
{{ .ID }}. {{ .Title }} by @{{ .Owner.UserName }}{{ if not .Assignee }}
/assign_{{ .ID }}
{{ else }}
{{ if eq .Assignee.ID $.CurrentUserID }}assignee: я
/unassign_{{ .ID }} /resolve_{{ .ID }}{{ else }}assignee: @{{ .Assignee.UserName }}{{ end }}
{{ end }}{{ end }}
`
const myTmpl = `
{{ range . }}
{{ .ID }}. {{ .Title }} by @{{ .Owner.UserName }}
/unassign_{{ .ID }} /resolve_{{ .ID }}
{{ end }}
`
const ownerTmpl = `
{{ range . }}
{{ .ID }}. {{ .Title }} by @{{ .Owner.UserName }}
/assign_{{ .ID }}
{{ end }}
`

func buildTextFromTemplate(tpl string, data interface{}) (string, error) {
	t := template.Must(template.New("tpl").Parse(tpl))
	builder := &strings.Builder{}
	if err := t.Execute(builder, data); err != nil {
		return "", fmt.Errorf("can't execute template: %v", err)
	}
	return strings.TrimSpace(builder.String()), nil
}

func GetPort() string {
	var port = os.Getenv("PORT")
	// Set a default port if there is nothing in the environment
	if port == "" {
		port = "8081"
		fmt.Println("INFO: No PORT environment variable detected, defaulting to " + port)
	}
	return ":" + port
}

func startTaskBot(ctx context.Context) error {
	tasks := newTasksRepo()

	bot, err := tgbotapi.NewBotAPI(BotToken)
	if err != nil {
		return err
	}

	updates := bot.ListenForWebhook("/")

	srv := &http.Server{Addr: GetPort()}
	go func() {
		<-ctx.Done()
		fmt.Println("Shutting down the HTTP server...")
		srv.Shutdown(ctx)
	}()

	go func() {
		log.Println("start listen :8081")
		if err := srv.ListenAndServe(); err != nil {
			log.Fatalf("listenAndServe failed: %v", err)
		}
	}()

	bot.Debug = true
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	for update := range updates {
		if update.Message == nil {
			continue
		}

		currentUser := &User{
			ID:       update.Message.Chat.ID,
			UserName: update.Message.Chat.UserName,
		}

		msg := tgbotapi.NewMessage(update.Message.Chat.ID, "")
		log.Printf("[%s] %s", update.Message.From.UserName, update.Message.Text)

		switch {
		case strings.HasPrefix(update.Message.Text, "/tasks"):
			if len(tasks.ByUser(currentUser)) == 0 {
				msg.Text = "Нет задач"
			} else {
				data := map[string]interface{}{
					"Tasks":         tasks.UnresolvedTasks(),
					"CurrentUserID": currentUser.ID,
				}

				text, err := buildTextFromTemplate(tasksTmpl, data)
				if err != nil {
					return err
				}
				msg.Text = text
			}
			bot.Send(msg)

		case strings.HasPrefix(update.Message.Text, "/new"):
			taskTitle := strings.TrimSpace(strings.TrimPrefix(update.Message.Text, "/new"))
			task := tasks.Add(taskTitle, currentUser)
			msg.Text = fmt.Sprintf("Задача \"%s\" создана, id=%d", task.Title, task.ID)
			bot.Send(msg)

		case strings.HasPrefix(update.Message.Text, "/assign_"):
			strTaskID := strings.Split(update.Message.Text, "_")[1]
			taskID, err := strconv.ParseInt(strTaskID, 10, 64)
			if err != nil {
				return err
			}

			task, found := tasks.Find(taskID)
			if !found {
				return fmt.Errorf("command /assign: task not found")
			}

			prevAssign := task.Assignee
			task.Assign(currentUser)
			text := fmt.Sprintf("Задача \"%s\" назначена на @%s", task.Title, currentUser.UserName)
			var senderID int64
			if prevAssign == nil {
				senderID = task.Owner.ID
			} else {
				senderID = prevAssign.ID
			}
			msgSender := tgbotapi.NewMessage(senderID, "")
			msgSender.Text = text
			bot.Send(msgSender)

			msg.Text = fmt.Sprintf("Задача \"%s\" назначена на вас", task.Title)
			bot.Send(msg)

		case strings.HasPrefix(update.Message.Text, "/unassign_"):
			strTaskID := strings.Split(update.Message.Text, "_")[1]
			taskID, err := strconv.ParseInt(strTaskID, 10, 64)
			if err != nil {
				return err
			}

			task, found := tasks.Find(taskID)
			if !found {
				return fmt.Errorf("command /unassign: task not found")
			}

			prevAssign := task.Assignee
			if currentUser.ID == prevAssign.ID {
				task.UnassignTask()

				msgOwner := tgbotapi.NewMessage(task.Owner.ID, "")
				msgOwner.Text = fmt.Sprintf("Задача \"%s\" осталась без исполнителя", task.Title)
				bot.Send(msgOwner)

				msgAssign := tgbotapi.NewMessage(prevAssign.ID, "")
				msgAssign.Text = "Принято"
				bot.Send(msgAssign)
			} else {
				msg.Text = "Задача не на вас"
				bot.Send(msg)
			}

		case strings.HasPrefix(update.Message.Text, "/resolve_"):
			strTaskID := strings.Split(update.Message.Text, "_")[1]
			taskID, err := strconv.ParseInt(strTaskID, 10, 64)
			if err != nil {
				return err
			}

			task, found := tasks.Find(taskID)
			if !found {
				return fmt.Errorf("command /unassign: task not found")
			}

			task.ResolveTask()

			msgOwner := tgbotapi.NewMessage(task.Owner.ID, "")
			msgOwner.Text = fmt.Sprintf("Задача \"%s\" выполнена @%s", task.Title, task.Assignee.UserName)
			bot.Send(msgOwner)

			msgAssign := tgbotapi.NewMessage(task.Assignee.ID, "")
			msgAssign.Text = fmt.Sprintf("Задача \"%s\" выполнена", task.Title)
			bot.Send(msgAssign)

		case strings.HasPrefix(update.Message.Text, "/my"):
			text, err := buildTextFromTemplate(myTmpl, tasks.ByAssignee(currentUser))
			if err != nil {
				return err
			}
			msg.Text = text
			bot.Send(msg)

		case strings.HasPrefix(update.Message.Text, "/owner"):
			text, err := buildTextFromTemplate(ownerTmpl, tasks.ByOwner(currentUser))
			if err != nil {
				return err
			}
			msg.Text = text
			bot.Send(msg)

		default:
			msg.Text = "Неизвестная команда"
			bot.Send(msg)
		}
	}

	return nil
}

func main() {
	ctx, finish := context.WithCancel(context.Background())
	defer finish()

	err := startTaskBot(ctx)
	if err != nil {
		log.Fatal(err)
	}
}
