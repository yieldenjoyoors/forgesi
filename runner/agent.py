#!/usr/bin/env python3
"""ForgeSI fleet agent.

Pulls a run from the knot, clones the repo, does the work with an LLM,
pushes the branch with its minted token, reports done. The same script
runs in the fleet container and in local development.

Env:
  KNOT        knot origin (default http://127.0.0.1:8797)
  LLM_URL     OpenAI-compatible chat endpoint
  LLM_KEY     bearer for the endpoint
  LLM_MODEL   model name
"""

import json
import os
import subprocess
import sys
import urllib.request

KNOT = os.environ.get("KNOT", "http://127.0.0.1:8797")
LLM_URL = os.environ.get("LLM_URL", "https://api.z.ai/api/coding/paas/v4/chat/completions")
LLM_KEY = os.environ.get("LLM_KEY", "")
LLM_MODEL = os.environ.get("LLM_MODEL", "glm-4.7")


def log(*a):
    print(*a, file=sys.stderr, flush=True)


def http(method, path, body=None, token=None):
    req = urllib.request.Request(KNOT + path, method=method)
    req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    data = json.dumps(body).encode() if body is not None else None
    with urllib.request.urlopen(req, data) as res:
        return json.loads(res.read() or b"{}")


def chat(prompt):
    body = {
        "model": LLM_MODEL,
        "messages": [
            {"role": "system", "content": (
                "You are a coding agent working in a git repository. "
                "Reply ONLY with a JSON object: {\"done\": bool, "
                "\"summary\": string (what you did, one line), "
                "\"files\": {\"path\": \"full new file content\", ...}}. "
                "Write complete file contents for every file you create or "
                "change; omit files you don't touch. When the task is "
                "complete, set done=true. Never run git commands.")},
            {"role": "user", "content": prompt},
        ],
    }
    req = urllib.request.Request(LLM_URL, method="POST")
    req.add_header("Content-Type", "application/json")
    req.add_header("Authorization", "Bearer " + LLM_KEY)
    with urllib.request.urlopen(req, json.dumps(body).encode()) as res:
        out = json.loads(res.read())
    text = out["choices"][0]["message"]["content"].strip()
    # tolerate markdown fences
    if text.startswith("```"):
        text = text.strip("`").lstrip("json").strip()
    return json.loads(text)


def sh(*args, cwd=None):
    r = subprocess.run(args, cwd=cwd, capture_output=True, text=True)
    if r.returncode != 0:
        raise RuntimeError(f"{args}: {r.stderr}")
    return r.stdout.strip()


def main():
    run = http("GET", "/xrpc/org.tangled.fleet.nextRun", token=os.environ.get("RUNNER_KEY", ""))
    log("run:", run["runId"], run["repo"], "branch", run["branch"], "-", run["todoTitle"])

    work = "/tmp/fleet-" + run["runId"]
    sh("git", "clone", "-q", f"{KNOT}/{run['repo']}", work)
    base = sh("git", "rev-parse", "HEAD", cwd=work)
    sh("git", "checkout", "-q", "-b", run["branch"], cwd=work)

    listing = sh("find", ".", "-type", "f", "-not", "-path", "./.git/*", cwd=work)
    step, files = 0, {}
    prompt = (
        f"Repository files:\n{listing}\n\nTask: {run['todoTitle']}\n"
        "Do the task. Reply with the JSON described."
    )
    while step < 5:
        step += 1
        out = chat(prompt)
        files.update(out.get("files") or {})
        if out.get("done"):
            break
        prompt = "Continue the task. Reply with the JSON described."

    if not files:
        http("POST", "/xrpc/org.tangled.fleet.runDone", token=os.environ.get("RUNNER_KEY", ""),
             body={"runId": run["runId"], "status": "failed",
                   "summary": "agent produced no changes"})
        log("no changes; reported failed")
        return

    for path, content in files.items():
        full = os.path.join(work, path)
        os.makedirs(os.path.dirname(full) or work, exist_ok=True)
        with open(full, "w") as f:
            f.write(content)
    sh("git", "add", "-A", cwd=work)
    sh("git", "-c", "user.name=fleet-agent", "-c", "user.email=agent@knot",
       "commit", "-q", "-m", "agent: " + run["todoTitle"], cwd=work)
    head = sh("git", "rev-parse", "HEAD", cwd=work)
    sh("git", "-c", f"http.extraHeader=Authorization: Bearer {run['token']}",
       "push", "-q", f"{KNOT}/{run['repo']}", f"HEAD:refs/heads/{run['branch']}", cwd=work)
    log("pushed", head)

    http("POST", "/xrpc/org.tangled.fleet.runDone", token=os.environ.get("RUNNER_KEY", ""),
         body={"runId": run["runId"], "status": "done",
               "summary": run["todoTitle"], "branch": run["branch"],
               "base": base, "head": head})
    log("done")


if __name__ == "__main__":
    main()
