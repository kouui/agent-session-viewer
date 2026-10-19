# TEMP plan: claude backend + subagent jump (git-excluded; keep updated)
Decisions (user): Claude = tokens only, no cost; whether Claude tokens count in plots is configurable (toggle per agent, Costs page; mini pages follow); no context % for Claude (used tokens only);
codex subagent link -> GitHub issue #1 (done). Order: 1 opencode jump [x done, commit pending], 2 Claude backend, 3 Claude jump + workflows.
Claude format facts: ~/.claude/projects/<cwd-encoded>/<sid>.jsonl (dir name lossy: take cwd from records); lines type user/assistant/system(turn_duration)/ai-title/cost-state/attachment/api-request*(skip);
one API message = several lines sharing message.id (dedupe usage by message.id); tool_result in a later user line; subagents <sid>/subagents/agent-<id>.jsonl + .meta.json{toolUseId,description,agentType,model};
workflows: Workflow tool_use -> result {taskId,runId,workflowName,transcriptDir}; agents in <sid>/subagents/workflows/wf_<runId>/agent-*.jsonl(+meta: agentType workflow-subagent, workflowPhase, model) + journal.jsonl; run record <sid>/workflows/wf_<runId>.json (status, agentCount, phases, totalTokens, result); up to 301 agents/workflow, 1029 agents in 63 workflows (aska-remake-2.5d).
Running: no turn_duration after last turn + recent mtime. Open question for user: group workflow agents under a collapsible workflow node (recommended) vs flat child sessions.
