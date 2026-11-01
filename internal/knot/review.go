package knot

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/yieldenjoyoors/forgesi/internal/gitrepo"
)

// The machine reviewer: after a proposal opens, the reviewer model reads
// the diff and posts a verdict as a comment on the ticket — before any
// human looks. Humans stay the merge gate; the verdict is triage.

const reviewSystem = `You are a code reviewer for an agent fleet. You read a
proposed change (old vs new files) and write a short verdict for a human
maintainer. Reply with 2-4 sentences: what the change does, whether it
looks correct and complete, and anything to check before merging.`

// reviewAsync triages a proposal. Runs in a goroutine: the HTTP response
// to runDone never waits on the model.
func (k *Knot) reviewAsync(repo, ticketURI, ticketCID, base, head, summary string) {
	if k.opt.ReviewLLM == nil {
		return
	}
	waitUntilHook(func() {
		defer func() {
			if rec := recover(); rec != nil {
				fmt.Fprintln(os.Stderr, "review panicked:", rec)
			}
		}()
		gr := gitrepo.Repo{Be: k.be, RefPrefix: "git/" + repo + "/"}
		prompt, err := diffSummary(gr, base, head)
		if err != nil {
			prompt = fmt.Sprintf("Proposal summary: %s\n(diff unavailable: %v)", summary, err)
		}
		verdict, err := k.opt.ReviewLLM(reviewSystem,
			fmt.Sprintf("Proposal: %s\n\n%s", summary, prompt))
		if err != nil {
			verdict = fmt.Sprintf("⚠️ reviewer unavailable: %v", err)
		}
		k.postComment(repo, ticketURI, ticketCID, verdict, "did:web:"+k.opt.Host+":reviewer")
	})
}

// diffSummary renders old/new content for every file that changed between
// base and head trees (truncated — enough for triage, not a full diff).
func diffSummary(gr gitrepo.Repo, base, head string) (string, error) {
	if base == "" || head == "" {
		return "", fmt.Errorf("missing base or head")
	}
	oldFiles, err := gr.Materialize(base)
	if err != nil {
		return "", err
	}
	newFiles, err := gr.Materialize(head)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	seen := map[string]bool{}
	for _, files := range []map[string][]byte{newFiles, oldFiles} {
		for p := range files {
			if !seen[p] {
				seen[p] = true
				old, hasOld := oldFiles[p]
				new, hasNew := newFiles[p]
				sb.WriteString("## " + p + "\n")
				switch {
				case !hasOld:
					fmt.Fprintf(&sb, "ADDED:\n%s\n", truncate(new, 1200))
				case !hasNew:
					sb.WriteString("REMOVED\n")
				case string(old) == string(new):
					sb.WriteString("unchanged\n")
				default:
					fmt.Fprintf(&sb, "OLD:\n%s\nNEW:\n%s\n", truncate(old, 800), truncate(new, 1200))
				}
			}
		}
	}
	return sb.String(), nil
}

func truncate(b []byte, n int) string {
	s := string(b)
	if len(s) > n {
		return s[:n] + "\n… (truncated)"
	}
	return s
}

// postComment writes a comment record on a ticket and emits the event.
func (k *Knot) postComment(repo, ticketURI, ticketCID, text, editor string) {
	comment := map[string]any{
		"$type":     "org.tangled.feed.comment",
		"subject":   map[string]any{"$type": "com.atproto.repo.strongRef", "uri": ticketURI, "cid": ticketCID},
		"body":      map[string]any{"$type": "org.tangled.markup.markdown", "text": text},
		"createdAt": time.Now().UTC().Format(time.RFC3339),
	}
	w, err := k.store.Create(repo, "org.tangled.feed.comment", "", comment, editor)
	if err != nil {
		return
	}
	k.emit(repo, "commit", "org.tangled.feed.comment", w.Rkey, w.CID, w.Change, comment)
}
