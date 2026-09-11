// A real audited delegation against a local Chat Completions server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/internal/workflow"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/prompt"
	"github.com/stevemurr/strap/provider/chatcompletions"
	"github.com/stevemurr/strap/work"
)

func run(ctx context.Context, baseURL, model string) (err error) {
	p, err := chatcompletions.New(chatcompletions.Config{BaseURL: baseURL, Model: model})
	if err != nil {
		return err
	}
	c := conversation.New(ctx)
	implementor := agent.Spec{Provider: p, Prompt: prompt.Prompt{Role: "Answer the assigned arithmetic question.", Instructions: []string{"Read the work envelope. Submit your answer with submit_work using its work_id and revision as expected_revision. Plain text alone does not submit. For repairs, address the findings and submit again."}}}
	auditor := agent.Spec{Provider: p, Prompt: prompt.Prompt{Role: "Audit the arithmetic submission.", Instructions: []string{"Read get_work for your assigned work_id and submission. Independently calculate the answer. Call submit_audit with your work_id, expected_revision, submission_id, summary, and verdict pass or fail. Fail requires findings with description, required_change, and verification. If unable to verify, set your blocker through update_work."}}}
	s := workflow.New(ctx, c, implementor, auditor)
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		err = errors.Join(err, s.Close(cleanup))
	}()
	_, err = c.CreateAgent(message.User, agent.Spec{Provider: p, Tools: s.RootTools(), Prompt: prompt.Prompt{Role: "Coordinate one audited arithmetic task.", Instructions: []string{"Call assign_work kind implementation once with task: calculate two plus two. After review_requested, call assign_work kind audit with the original work_id, current expected_revision and submission_id; use get_work to refresh. Repairs are issued automatically on failed audit. Assign a new audit after repair submission. Wait for acceptance before claiming success. Do not poll; events arrive automatically."}}})
	if err != nil {
		return err
	}
	if _, err = c.Send(c.Root(), "Delegate and audit the answer to two plus two."); err != nil {
		return err
	}
	for {
		event, e := s.NextEvent(ctx)
		if e != nil {
			return e
		}
		switch event := event.(type) {
		case conversation.WorkEvent:
			fmt.Printf("%s: %s (%s)\n", event.Event.Work.ID, event.Event.Kind, event.Event.Work.State)
			if event.Event.Kind == work.AuditCompleted && event.Event.Work.State == work.Accepted {
				fmt.Println("Verified audited delegation flow.")
				return nil
			}
		case conversation.MessageEvent:
			if event.Message.Kind == message.Failure {
				return fmt.Errorf("%s", event.Message.Content)
			}
			if event.Message.To == message.User {
				fmt.Println(event.Message.Content)
			}
		}
	}
}
func main() {
	baseURL := flag.String("base-url", "http://127.0.0.1:1234", "Local server root or API prefix")
	model := flag.String("model", "qwen/qwen3-vl-4b", "Model identifier served by the local endpoint")
	timeout := flag.Duration("timeout", 5*time.Minute, "Overall demonstration deadline")
	flag.Parse()
	interrupt, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(interrupt, *timeout)
	defer cancel()
	if err := run(ctx, *baseURL, *model); err != nil {
		log.Fatal(err)
	}
}
