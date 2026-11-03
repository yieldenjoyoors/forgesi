package agent

// The repo drives the agent. AGENTS.md (the convention) is always
// respected; .agents/workflow.md customizes how the agent works; skills
// under .agents/skills/ load on mention. The JSON wire contract stays
// fixed — a repo can steer behavior, not break the protocol.

import (
	"path"
	"sort"
	"strings"
)

const skillsDir = ".agents/skills/"

// agentContext is the repo-driven prompt material.
type agentContext struct {
	agentsMD  string // AGENTS.md: conventions the agent must respect
	workflow  string // .agents/workflow.md: repo-specific working rules
	skillIdx  []string
	skillsAll map[string]string // skill name -> full markdown
}

// readAgentContext pulls the workflow material out of the cloned tree.
func readAgentContext(files map[string][]byte) *agentContext {
	ac := &agentContext{skillsAll: map[string]string{}}
	if v, ok := files["AGENTS.md"]; ok {
		ac.agentsMD = string(v)
	}
	if v, ok := files[".agents/workflow.md"]; ok {
		ac.workflow = string(v)
	}
	for p, v := range files {
		if !strings.HasPrefix(p, skillsDir) {
			continue
		}
		name := skillName(p)
		if name == "" {
			continue
		}
		ac.skillsAll[name] = string(v)
	}
	for name := range ac.skillsAll {
		ac.skillIdx = append(ac.skillIdx, name)
	}
	sort.Strings(ac.skillIdx)
	return ac
}

// skillName: .agents/skills/<name>.md -> <name>;
// .agents/skills/<name>/SKILL.md -> <name>.
func skillName(p string) string {
	rest := strings.TrimPrefix(p, skillsDir)
	if strings.HasPrefix(rest, ".") {
		return ""
	}
	if strings.HasSuffix(rest, ".md") {
		return strings.TrimSuffix(rest, ".md")
	}
	if path.Base(rest) == "SKILL.md" {
		return strings.TrimSuffix(path.Dir(rest), "/")
	}
	return ""
}

// systemFor builds the system prompt: the wire contract, then the
// repo's conventions and workflow, then the skill index.
func (ac *agentContext) systemFor() string {
	var sb strings.Builder
	sb.WriteString(systemPrompt)
	if ac.agentsMD != "" {
		sb.WriteString("\n\n# Repo instructions (AGENTS.md) — respect these:\n\n")
		sb.WriteString(strings.TrimSpace(ac.agentsMD))
	}
	if ac.workflow != "" {
		sb.WriteString("\n\n# Repo agent workflow (.agents/workflow.md) — follow these working rules:\n\n")
		sb.WriteString(strings.TrimSpace(ac.workflow))
	}
	if len(ac.skillIdx) > 0 {
		sb.WriteString("\n\n# Repo skills (mention a skill by name in the task to load it):\n- ")
		sb.WriteString(strings.Join(ac.skillIdx, "\n- "))
	}
	return sb.String()
}

// taskFor augments the task with the full text of any skill the task
// mentions by name.
func (ac *agentContext) taskFor(task string) string {
	var loaded []string
	for name, body := range ac.skillsAll {
		if strings.Contains(strings.ToLower(task), strings.ToLower(name)) {
			loaded = append(loaded, "# Skill: "+name+"\n\n"+strings.TrimSpace(body))
		}
	}
	if len(loaded) == 0 {
		return task
	}
	sort.Strings(loaded)
	return task + "\n\n-- loaded skills --\n\n" + strings.Join(loaded, "\n\n")
}
