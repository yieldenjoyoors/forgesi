package knot

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/yieldenjoyoors/forgesi/internal/forgeci"
	"github.com/yieldenjoyoors/forgesi/internal/gitrepo"
)

// The machine reviewer: after a proposal opens, the reviewer model reads
// the diff and posts a verdict as a comment on the ticket — before any
// human looks. Humans stay the merge gate; the verdict is triage.

const reviewSystem = `You are a code reviewer for an agent fleet. You read a
proposed change (old vs new files) and write a short verdict for a human
maintainer. Reply with 2-4 sentences: what the change does, whether it
looks correct and complete, and anything to check before merging.`

const reviewAutoSuffix = `
This proposal may merge automatically when you do not object: if a human
must look at it, begin your verdict with "BLOCK:". Otherwise describe the
change and confirm it is correct and complete.`

// reviewAsync triages a proposal. Runs in a goroutine: the HTTP response
// to runDone never waits on the model. The repo's forgeci.json decides
// whether a clear verdict ALSO auto-merges (agent review alone) or the
// proposal waits for a human.
func (k *Knot) reviewAsync(repo, ticketURI, ticketCID, base, head, summary string) {
	if k.opt.ReviewLLM == nil {
		return
	}
	// waitUntil survives the client disconnecting (the runner aborts
	// its runDone fetch after 30s); a synchronous review would be
	// cancelled with it.
	waitUntilHook(func() {
		defer func() {
			if rec := recover(); rec != nil {
				fmt.Fprintln(os.Stderr, "review panicked:", rec)
			}
		}()
		gr := k.gitRepoFor(repo)
		dec := policyDecision(gr, base, head)
		system := reviewSystem
		if !dec.HumanRequired {
			system += reviewAutoSuffix
		}
		prompt, err := diffSummary(gr, base, head)
		if err != nil {
			prompt = fmt.Sprintf("Proposal summary: %s\n(diff unavailable: %v)", summary, err)
		}
		prompt += "\n\nReview policy: " + dec.Reason
		verdict, err := k.opt.ReviewLLM(system,
			fmt.Sprintf("Proposal: %s\n\n%s", summary, prompt))
		if err != nil {
			verdict = fmt.Sprintf("⚠️ reviewer unavailable: %v", err)
		}
		k.postComment(repo, ticketURI, ticketCID, verdict, "did:web:"+k.opt.Host+":reviewer")

		// the agent-reviewed path: policy clears it and the reviewer did
		// not object — merge now, flip the todo line, say so.
		if err == nil && !dec.HumanRequired && !strings.HasPrefix(strings.TrimSpace(verdict), "BLOCK") {
			k.autoMerge(repo, ticketURI, ticketCID, base, head, dec)
		} else if err == nil && dec.HumanRequired {
			k.postComment(repo, ticketURI, ticketCID,
				"⏳ forgeci policy: human approval required — "+dec.Reason,
				"did:web:"+k.opt.Host+":reviewer")
		}
	})
}

// autoMerge lands the proposal on main under the knot's own authority
// and completes the todo line via the ticket. Failure is a comment,
// never a panic — conflicts mean re-run the agent.
func (k *Knot) autoMerge(repo, ticketURI, ticketCID, base, head string, dec forgeci.Decision) {
	_ = base // the merge targets current main, matching handleMerge's default
	rkey := strings.TrimPrefix(ticketURI, "at://"+repo+"/org.tangled.track.ticket/")
	gr := k.gitrepoRepo(repo)
	commit, ff, err := mergeRepo(gr, "main", head)
	if err != nil {
		k.postComment(repo, ticketURI, ticketCID,
			"⚠️ auto-merge failed: "+err.Error(), "did:web:"+k.opt.Host+":reviewer")
		return
	}
	editor := k.selfDID()
	k.emit(repo, "commit", "", "refs/heads/main", commit, commit,
		map[string]any{"merge": head, "editor": editor, "auto": true})
	k.completeTodoOnMerge(repo, rkey, editor)
	note := "🤖 auto-merged " + commit[:8]
	if ff {
		note += " (fast-forward)"
	}
	k.postComment(repo, ticketURI, ticketCID, note+" — forgeci policy: "+dec.Reason, editor)
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
	w, err := k.storeFor(repo).Create(repo, "org.tangled.feed.comment", "", comment, editor)
	if err != nil {
		return
	}
	k.emit(repo, "commit", "org.tangled.feed.comment", w.Rkey, w.CID, w.Change, comment)
}
