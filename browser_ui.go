package main

import (
	"html/template"
	"net/url"
	"strconv"
	"strings"
)

// browserLane is deliberately derived from the issue graph on every request.
// There is no second board configuration to drift from the API's parent and
// position relationships.
type browserLane struct {
	ID     string
	Parent *Issue
	Issues []Issue
}

var browserColumns = []string{"unclaimed", "in-progress", "blocked", "done"}

func renderProjectBoard(project Project, issues []Issue) string {
	lanes := buildBrowserLanes(issues)
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>`)
	b.WriteString(template.HTMLEscapeString(project.Name))
	b.WriteString(` · SimpleTracker</title>`)
	b.WriteString(browserCSS)
	b.WriteString(`</head><body data-project-slug="`)
	b.WriteString(template.HTMLEscapeString(project.Slug))
	b.WriteString(`"><div class="app-shell"><header class="topbar"><div><a class="eyebrow" href="/">SimpleTracker</a><h1>`)
	b.WriteString(template.HTMLEscapeString(project.Name))
	b.WriteString(`</h1><p class="muted">`)
	b.WriteString(template.HTMLEscapeString(project.Slug))
	b.WriteString(` · parent issues shape the lanes</p></div><a class="button ghost" href="/">All projects</a></header><main>`)
	b.WriteString(`<section class="board-section" aria-labelledby="board-title"><div class="section-heading"><div><p class="eyebrow">Primary work surface</p><h2 id="board-title">Swimlane board</h2></div><span class="legend">Unclaimed · In progress · Blocked · Done</span></div><div class="board" data-board>`)
	for _, lane := range lanes {
		b.WriteString(renderBrowserLane(project, lane))
	}
	b.WriteString(`</div></section>`)
	b.WriteString(renderIssueList(project, issues))
	b.WriteString(`</main></div>`)
	b.WriteString(browserBoardScript)
	b.WriteString(`</body></html>`)
	return b.String()
}

func buildBrowserLanes(issues []Issue) []browserLane {
	lanes := make([]browserLane, 0)
	byID := make(map[string]int, len(issues))
	for i := range issues {
		if len(issues[i].Children) == 0 {
			continue
		}
		byID[issues[i].ID] = len(lanes)
		lanes = append(lanes, browserLane{ID: issues[i].ID, Parent: &issues[i]})
	}
	defaultLane := browserLane{ID: "unparented", Parent: nil}
	for _, issue := range issues {
		if issue.ParentID != "" {
			if laneIndex, ok := byID[issue.ParentID]; ok {
				lanes[laneIndex].Issues = append(lanes[laneIndex].Issues, issue)
				continue
			}
		}
		// Parent issues are represented by their lane header. A root issue
		// without children remains a normal card in the default lane.
		if issue.ParentID == "" && len(issue.Children) > 0 {
			continue
		}
		defaultLane.Issues = append(defaultLane.Issues, issue)
	}
	// Always show the default lane: it is useful when a project has only
	// parent issues and makes the board's grouping rule visible to humans.
	lanes = append(lanes, defaultLane)
	return lanes
}

func renderBrowserLane(project Project, lane browserLane) string {
	var b strings.Builder
	b.WriteString(`<section class="lane" data-board-lane="`)
	b.WriteString(template.HTMLEscapeString(lane.ID))
	b.WriteString(`"><header class="lane-header">`)
	if lane.Parent != nil {
		b.WriteString(`<div><p class="eyebrow">Parent lane</p><h3><a href="`)
		b.WriteString(browserIssueURL(*lane.Parent))
		b.WriteString(`">#`)
		b.WriteString(strconv.FormatInt(lane.Parent.Number, 10))
		b.WriteString(` `)
		b.WriteString(template.HTMLEscapeString(lane.Parent.Title))
		b.WriteString(`</a></h3></div><span class="lane-count">`)
		b.WriteString(strconv.Itoa(len(lane.Issues)))
		b.WriteString(` children</span>`)
	} else {
		b.WriteString(`<div><p class="eyebrow">Default lane</p><h3>Unparented issues</h3></div><span class="lane-count">`)
		b.WriteString(strconv.Itoa(len(lane.Issues)))
		b.WriteString(` issues</span>`)
	}
	b.WriteString(`</header><div class="lane-columns">`)
	for _, column := range browserColumns {
		b.WriteString(`<section class="board-column column-`)
		b.WriteString(column)
		b.WriteString(`" data-board-column="`)
		b.WriteString(column)
		b.WriteString(`"><header><h4>`)
		b.WriteString(browserColumnTitle(column))
		b.WriteString(`</h4><span class="column-count">`)
		count := 0
		for _, issue := range lane.Issues {
			if browserIssueColumn(issue) == column {
				count++
			}
		}
		b.WriteString(strconv.Itoa(count))
		b.WriteString(`</span></header><div class="card-stack">`)
		for _, issue := range lane.Issues {
			if browserIssueColumn(issue) == column {
				b.WriteString(renderIssueCard(issue))
			}
		}
		if count == 0 {
			b.WriteString(`<p class="empty-column">Drop work here</p>`)
		}
		b.WriteString(`</div></section>`)
	}
	b.WriteString(`</div></section>`)
	return b.String()
}

func browserIssueColumn(issue Issue) string {
	if issue.State == "closed" {
		return "done"
	}
	if issue.Blocked {
		return "blocked"
	}
	if strings.TrimSpace(issue.Assignee) != "" {
		return "in-progress"
	}
	return "unclaimed"
}

func browserColumnTitle(column string) string {
	switch column {
	case "in-progress":
		return "In progress"
	case "unclaimed":
		return "Unclaimed"
	case "blocked":
		return "Blocked"
	case "done":
		return "Done"
	default:
		return column
	}
}

func renderIssueCard(issue Issue) string {
	var b strings.Builder
	column := browserIssueColumn(issue)
	b.WriteString(`<article class="issue-card" data-issue-card data-issue-id="`)
	b.WriteString(template.HTMLEscapeString(issue.ID))
	b.WriteString(`" data-parent-id="`)
	b.WriteString(template.HTMLEscapeString(issue.ParentID))
	b.WriteString(`" data-column="`)
	b.WriteString(column)
	b.WriteString(`" draggable="true"><a class="card-link" draggable="false" href="`)
	b.WriteString(browserIssueURL(issue))
	b.WriteString(`"><div class="card-top"><span class="issue-number">#`)
	b.WriteString(strconv.FormatInt(issue.Number, 10))
	b.WriteString(`</span><span class="state-dot state-`)
	b.WriteString(column)
	b.WriteString(`" title="`)
	b.WriteString(browserColumnTitle(column))
	b.WriteString(`"></span></div><h5>`)
	b.WriteString(template.HTMLEscapeString(issue.Title))
	b.WriteString(`</h5>`)
	if len(issue.Labels) > 0 {
		b.WriteString(`<div class="chips">`)
		for _, label := range issue.Labels {
			b.WriteString(`<span class="chip">`)
			b.WriteString(template.HTMLEscapeString(label))
			b.WriteString(`</span>`)
		}
		b.WriteString(`</div>`)
	}
	b.WriteString(`<div class="card-meta">`)
	if issue.Assignee != "" {
		b.WriteString(`<span class="assignee">@`)
		b.WriteString(template.HTMLEscapeString(issue.Assignee))
		b.WriteString(`</span>`)
	} else {
		b.WriteString(`<span class="assignee unassigned">Unassigned</span>`)
	}
	if issue.Blocked {
		b.WriteString(`<span class="blocker-cue">`)
		b.WriteString(strconv.Itoa(len(issue.Blockers)))
		b.WriteString(` open blocker`)
		if len(issue.Blockers) != 1 {
			b.WriteString(`s`)
		}
		b.WriteString(`</span>`)
	}
	b.WriteString(`</div><time class="update-cue" datetime="`)
	b.WriteString(issue.UpdatedAt.Format(timeRFC3339))
	b.WriteString(`">Updated `)
	b.WriteString(issue.UpdatedAt.Format("02 Jan 15:04"))
	b.WriteString(`</time></a></article>`)
	return b.String()
}

func renderIssueList(project Project, issues []Issue) string {
	var b strings.Builder
	b.WriteString(`<section class="issue-list-section" aria-labelledby="issue-list-title"><div class="section-heading"><div><p class="eyebrow">Utility lookup</p><h2 id="issue-list-title">Issue list</h2></div><span class="muted">Search and filter the same query surface agents use.</span></div><form class="filters" data-issue-filters><label>Search<input type="search" name="q" data-filter-search placeholder="title or body"></label><label>State<select name="state" data-filter-state><option value="">Any state</option><option value="open">Open</option><option value="closed">Done</option></select></label><label>Assignment<select name="assigned" data-filter-assigned><option value="">Any assignment</option><option value="unassigned">Unclaimed</option><option value="assigned">Assigned</option></select></label><label>Parent<select name="parent" data-filter-parent><option value="">Any parent</option><option value="none">Unparented</option>`)
	for _, issue := range issues {
		if len(issue.Children) == 0 {
			continue
		}
		b.WriteString(`<option value="`)
		b.WriteString(template.HTMLEscapeString(issue.ID))
		b.WriteString(`">#`)
		b.WriteString(strconv.FormatInt(issue.Number, 10))
		b.WriteString(` `)
		b.WriteString(template.HTMLEscapeString(issue.Title))
		b.WriteString(`</option>`)
	}
	b.WriteString(`</select></label><label>Assignee<input type="search" name="assignee" data-filter-assignee placeholder="agent or person"></label><label>Label<input type="search" name="label" data-filter-label placeholder="label"></label><button class="button" type="submit">Apply filters</button><button class="button ghost" type="button" data-clear-filters>Clear</button></form><div class="table-wrap"><table class="issue-table"><thead><tr><th>Issue</th><th>State</th><th>Assignment</th><th>Parent</th><th>Updated</th></tr></thead><tbody data-issue-list-body>`)
	for _, issue := range issues {
		b.WriteString(renderIssueRow(issue))
	}
	b.WriteString(`</tbody></table></div></section>`)
	_ = project // Keep the renderer signature explicit for future project-scoped controls.
	return b.String()
}

func renderIssueRow(issue Issue) string {
	var b strings.Builder
	column := browserIssueColumn(issue)
	b.WriteString(`<tr data-issue-row data-state="`)
	b.WriteString(template.HTMLEscapeString(issue.State))
	b.WriteString(`" data-assigned="`)
	if issue.Assignee == "" {
		b.WriteString(`unassigned`)
	} else {
		b.WriteString(`assigned`)
	}
	b.WriteString(`" data-parent="`)
	if issue.ParentID == "" {
		b.WriteString(`none`)
	} else {
		b.WriteString(template.HTMLEscapeString(issue.ParentID))
	}
	b.WriteString(`" data-blocked="`)
	b.WriteString(strconv.FormatBool(issue.Blocked))
	b.WriteString(`"><td><a href="`)
	b.WriteString(browserIssueURL(issue))
	b.WriteString(`"><strong>#`)
	b.WriteString(strconv.FormatInt(issue.Number, 10))
	b.WriteString(`</strong> `)
	b.WriteString(template.HTMLEscapeString(issue.Title))
	b.WriteString(`</a><div class="chips">`)
	for _, label := range issue.Labels {
		b.WriteString(`<span class="chip subtle">`)
		b.WriteString(template.HTMLEscapeString(label))
		b.WriteString(`</span>`)
	}
	b.WriteString(`</div></td><td><span class="status-pill status-`)
	b.WriteString(column)
	b.WriteString(`">`)
	b.WriteString(browserColumnTitle(column))
	b.WriteString(`</span></td><td>`)
	if issue.Assignee == "" {
		b.WriteString(`<span class="muted">Unassigned</span>`)
	} else {
		b.WriteString(`@`)
		b.WriteString(template.HTMLEscapeString(issue.Assignee))
	}
	b.WriteString(`</td><td>`)
	if issue.Parent != nil {
		b.WriteString(`<a href="`)
		b.WriteString(browserIssueURL(Issue{ProjectSlug: issue.Parent.ProjectSlug, Number: issue.Parent.Number}))
		b.WriteString(`">#`)
		b.WriteString(strconv.FormatInt(issue.Parent.Number, 10))
		b.WriteString(` `)
		b.WriteString(template.HTMLEscapeString(issue.Parent.Title))
		b.WriteString(`</a>`)
	} else {
		b.WriteString(`<span class="muted">—</span>`)
	}
	b.WriteString(`</td><td><time datetime="`)
	b.WriteString(issue.UpdatedAt.Format(timeRFC3339))
	b.WriteString(`">`)
	b.WriteString(issue.UpdatedAt.Format("02 Jan 15:04"))
	b.WriteString(`</time></td></tr>`)
	return b.String()
}

func browserIssueURL(issue Issue) string {
	return "/projects/" + template.HTMLEscapeString(url.PathEscape(issue.ProjectSlug)) + "/issues/" + strconv.FormatInt(issue.Number, 10)
}

// Keep the layout stable for browser tests and clients that parse datetime
// attributes while avoiding another dependency for a one-line format string.
const timeRFC3339 = "2006-01-02T15:04:05Z07:00"

func renderInteractiveIssuePage(issue Issue) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>#`)
	b.WriteString(strconv.FormatInt(issue.Number, 10))
	b.WriteString(` `)
	b.WriteString(template.HTMLEscapeString(issue.Title))
	b.WriteString(` · SimpleTracker</title>`)
	b.WriteString(browserCSS)
	b.WriteString(`</head><body data-project-slug="`)
	b.WriteString(template.HTMLEscapeString(issue.ProjectSlug))
	b.WriteString(`" data-issue-id="`)
	b.WriteString(template.HTMLEscapeString(issue.ID))
	b.WriteString(`" data-issue-number="`)
	b.WriteString(strconv.FormatInt(issue.Number, 10))
	b.WriteString(`"><div class="app-shell detail-shell"><header class="topbar"><div><a class="eyebrow" href="/projects/`)
	b.WriteString(template.HTMLEscapeString(url.PathEscape(issue.ProjectSlug)))
	b.WriteString(`">← Back to board</a><h1>#`)
	b.WriteString(strconv.FormatInt(issue.Number, 10))
	b.WriteString(` `)
	b.WriteString(template.HTMLEscapeString(issue.Title))
	b.WriteString(`</h1><p class="muted">`)
	b.WriteString(template.HTMLEscapeString(issue.ProjectSlug))
	b.WriteString(` · `)
	b.WriteString(template.HTMLEscapeString(issue.State))
	b.WriteString(`</p></div><button class="button state-action" data-toggle-state>`)
	if issue.State == "closed" {
		b.WriteString(`Reopen issue`)
	} else {
		b.WriteString(`Close issue`)
	}
	b.WriteString(`</button></header><main class="detail-grid"><article class="detail-main"><section class="panel"><div class="panel-heading"><div><p class="eyebrow">Issue detail</p><h2>Markdown description</h2></div><span class="status-pill status-`)
	b.WriteString(browserIssueColumn(issue))
	b.WriteString(`">`)
	b.WriteString(browserColumnTitle(browserIssueColumn(issue)))
	b.WriteString(`</span></div><pre class="markdown-body" id="issue-markdown">`)
	b.WriteString(template.HTMLEscapeString(issue.Body))
	b.WriteString(`</pre><form data-issue-form class="issue-form"><label>Title<input name="title" value="`)
	b.WriteString(template.HTMLEscapeString(issue.Title))
	b.WriteString(`" required></label><label>Markdown body<textarea name="body" rows="12">`)
	b.WriteString(template.HTMLEscapeString(issue.Body))
	b.WriteString(`</textarea></label><button class="button" type="submit">Save issue</button><span class="form-message" data-form-message></span></form></section>`)
	b.WriteString(renderComments(issue))
	b.WriteString(`</article><aside class="detail-sidebar">`)
	b.WriteString(renderIssueRelationships(issue))
	b.WriteString(`</aside></main></div>`)
	b.WriteString(browserDetailScript)
	b.WriteString(`</body></html>`)
	return b.String()
}

func renderComments(issue Issue) string {
	var b strings.Builder
	b.WriteString(`<section class="panel"><div class="panel-heading"><div><p class="eyebrow">Activity</p><h2>Comments</h2></div><span class="muted">`)
	b.WriteString(strconv.Itoa(len(issue.Comments)))
	b.WriteString(`</span></div><div class="comment-list" data-comment-list>`)
	for _, comment := range issue.Comments {
		b.WriteString(`<article class="comment"><div class="comment-meta"><strong>`)
		if comment.Actor != "" {
			b.WriteString(template.HTMLEscapeString(comment.Actor))
		} else {
			b.WriteString(`Tracker user`)
		}
		b.WriteString(`</strong><time datetime="`)
		b.WriteString(comment.CreatedAt.Format(timeRFC3339))
		b.WriteString(`">`)
		b.WriteString(comment.CreatedAt.Format("02 Jan 15:04"))
		b.WriteString(`</time></div><pre>`)
		b.WriteString(template.HTMLEscapeString(comment.Body))
		b.WriteString(`</pre></article>`)
	}
	if len(issue.Comments) == 0 {
		b.WriteString(`<p class="empty-state">No comments yet.</p>`)
	}
	b.WriteString(`</div><form class="comment-form" data-comment-form><textarea name="body" rows="4" placeholder="Leave an update…" required></textarea><button class="button" type="submit">Add comment</button><span class="form-message" data-comment-message></span></form></section>`)
	return b.String()
}

func renderIssueRelationships(issue Issue) string {
	var b strings.Builder
	b.WriteString(`<section class="panel metadata-panel"><div class="panel-heading"><div><p class="eyebrow">Controls</p><h2>Assignment & labels</h2></div></div><form data-metadata-form class="issue-form"><label>Assignee<input name="assignee" value="`)
	b.WriteString(template.HTMLEscapeString(issue.Assignee))
	b.WriteString(`" placeholder="agent or person"></label><label>Labels<input name="labels" value="`)
	b.WriteString(template.HTMLEscapeString(strings.Join(issue.Labels, ", ")))
	b.WriteString(`" placeholder="comma-separated labels"></label><label>Parent issue<input name="parent_id" value="`)
	b.WriteString(template.HTMLEscapeString(issue.ParentID))
	b.WriteString(`" placeholder="issue id or project#number"></label><button class="button" type="submit">Save metadata</button><span class="form-message" data-metadata-message></span></form></section>`)
	b.WriteString(`<section class="panel relationship-panel"><div class="panel-heading"><div><p class="eyebrow">Graph</p><h2>Parent & children</h2></div></div>`)
	if issue.Parent != nil {
		b.WriteString(`<p class="relationship-row"><span>Parent</span><a href="`)
		b.WriteString(browserIssueURL(Issue{ProjectSlug: issue.Parent.ProjectSlug, Number: issue.Parent.Number}))
		b.WriteString(`">#`)
		b.WriteString(strconv.FormatInt(issue.Parent.Number, 10))
		b.WriteString(` `)
		b.WriteString(template.HTMLEscapeString(issue.Parent.Title))
		b.WriteString(`</a></p>`)
	} else {
		b.WriteString(`<p class="relationship-row"><span>Parent</span><span class="muted">Unparented</span></p>`)
	}
	b.WriteString(`<h3>Children</h3><ul class="link-list">`)
	for _, child := range issue.Children {
		b.WriteString(`<li><a href="`)
		b.WriteString(browserIssueURL(Issue{ProjectSlug: child.ProjectSlug, Number: child.Number}))
		b.WriteString(`">#`)
		b.WriteString(strconv.FormatInt(child.Number, 10))
		b.WriteString(` `)
		b.WriteString(template.HTMLEscapeString(child.Title))
		b.WriteString(`</a><span class="muted">`)
		b.WriteString(template.HTMLEscapeString(child.State))
		b.WriteString(`</span></li>`)
	}
	if len(issue.Children) == 0 {
		b.WriteString(`<li class="muted">No children</li>`)
	}
	b.WriteString(`</ul></section><section class="panel relationship-panel"><div class="panel-heading"><div><p class="eyebrow">Dependencies</p><h2>Blockers</h2></div><span class="status-pill `)
	if issue.Blocked {
		b.WriteString(`status-blocked">Blocked`)
	} else {
		b.WriteString(`status-unclaimed">Clear`)
	}
	b.WriteString(`</span></div><ul class="link-list" data-blocker-list>`)
	for _, blocker := range issue.Blockers {
		b.WriteString(`<li><a href="`)
		b.WriteString(browserIssueURL(Issue{ProjectSlug: blocker.ProjectSlug, Number: blocker.Number}))
		b.WriteString(`">#`)
		b.WriteString(strconv.FormatInt(blocker.Number, 10))
		b.WriteString(` `)
		b.WriteString(template.HTMLEscapeString(blocker.Title))
		b.WriteString(`</a><button class="icon-button" type="button" data-remove-blocker="`)
		b.WriteString(template.HTMLEscapeString(blocker.ID))
		b.WriteString(`" aria-label="Remove blocker">×</button></li>`)
	}
	if len(issue.Blockers) == 0 {
		b.WriteString(`<li class="muted">No open blockers</li>`)
	}
	b.WriteString(`</ul><h3>Blocked issues</h3><ul class="link-list" data-blocked-issues>`)
	for _, blocked := range issue.BlockedIssues {
		b.WriteString(`<li><a href="`)
		b.WriteString(browserIssueURL(Issue{ProjectSlug: blocked.ProjectSlug, Number: blocked.Number}))
		b.WriteString(`">#`)
		b.WriteString(strconv.FormatInt(blocked.Number, 10))
		b.WriteString(` `)
		b.WriteString(template.HTMLEscapeString(blocked.Title))
		b.WriteString(`</a><span class="muted">`)
		b.WriteString(template.HTMLEscapeString(blocked.State))
		b.WriteString(`</span></li>`)
	}
	if len(issue.BlockedIssues) == 0 {
		b.WriteString(`<li class="muted">No blocked issues</li>`)
	}
	b.WriteString(`</ul><form class="inline-form" data-blocker-form><input name="blocker_id" placeholder="issue id or project#number" required><button class="button" type="submit">Add blocker</button></form><span class="form-message" data-blocker-message></span></section>`)
	return b.String()
}

const browserCSS = `<style>
:root{color-scheme:light;--ink:#18212b;--muted:#657282;--line:#dfe5ec;--panel:#fff;--wash:#f4f6f8;--blue:#1f5eff;--shadow:0 16px 35px rgba(35,49,67,.08)}*{box-sizing:border-box}body{margin:0;background:#f7f8fa;color:var(--ink);font:14px/1.45 ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif}a{color:#2456bd}button,input,textarea,select{font:inherit}button{cursor:pointer}.app-shell{max-width:1680px;margin:0 auto;padding:28px 32px 64px}.topbar{display:flex;align-items:flex-start;justify-content:space-between;gap:24px;padding:8px 0 34px}.topbar h1{margin:4px 0 0;font-size:clamp(1.8rem,3vw,2.75rem);letter-spacing:-.04em}.eyebrow{text-transform:uppercase;letter-spacing:.12em;font-size:11px;font-weight:750;color:#6d7785;margin:0}.muted{color:var(--muted)}.button{background:var(--blue);border:1px solid var(--blue);border-radius:8px;color:#fff;font-weight:700;padding:9px 14px}.button.ghost{background:#fff;border-color:var(--line);color:#334155}.section-heading{align-items:end;display:flex;justify-content:space-between;gap:24px;margin:16px 0}.section-heading h2{font-size:1.4rem;letter-spacing:-.025em;margin:2px 0}.legend{color:var(--muted);font-size:12px}.board{display:flex;flex-direction:column;gap:20px}.lane{background:var(--panel);border:1px solid var(--line);border-radius:14px;box-shadow:var(--shadow);overflow:hidden}.lane-header{align-items:center;background:#fbfcfd;border-bottom:1px solid var(--line);display:flex;justify-content:space-between;padding:15px 18px}.lane-header h3{font-size:1rem;margin:2px 0 0}.lane-header a{text-decoration:none;color:inherit}.lane-count,.column-count{color:var(--muted);font-size:12px}.lane-columns{display:grid;grid-template-columns:repeat(4,minmax(180px,1fr));overflow-x:auto}.board-column{border-right:1px solid var(--line);min-height:170px;padding:11px}.board-column:last-child{border-right:0}.board-column>header{align-items:center;display:flex;justify-content:space-between;margin:0 2px 10px}.board-column h4{font-size:12px;margin:0;text-transform:uppercase;letter-spacing:.08em}.card-stack{display:flex;flex-direction:column;gap:9px;min-height:120px}.issue-card{background:#fff;border:1px solid var(--line);border-radius:9px;box-shadow:0 3px 10px rgba(27,39,55,.05);cursor:grab}.issue-card:active{cursor:grabbing}.issue-card.dragging{opacity:.4}.card-link{color:inherit;display:block;padding:12px;text-decoration:none}.card-top,.card-meta{align-items:center;display:flex;gap:8px;justify-content:space-between}.issue-number{color:var(--muted);font-size:12px;font-variant-numeric:tabular-nums}.state-dot{border-radius:50%;height:8px;width:8px}.state-unclaimed{background:#94a3b8}.state-in-progress{background:#5c7cfa}.state-blocked{background:#ed6a5a}.state-done{background:#37a26e}.issue-card h5{font-size:14px;line-height:1.35;margin:7px 0 9px}.chips{display:flex;flex-wrap:wrap;gap:4px}.chip{background:#edf2ff;border-radius:999px;color:#3153a6;font-size:11px;padding:3px 7px}.chip.subtle{background:#f0f2f4;color:#56616f}.card-meta{font-size:11px;margin-top:10px}.assignee{color:#3a4e75}.assignee.unassigned,.blocker-cue{color:#b14c3e}.update-cue{color:var(--muted);display:block;font-size:10px;margin-top:9px}.empty-column{border:1px dashed #ccd5df;border-radius:8px;color:#99a3af;font-size:12px;margin:0;padding:22px 8px;text-align:center}.issue-list-section{margin-top:48px}.filters{align-items:end;background:#fff;border:1px solid var(--line);border-radius:10px;display:flex;flex-wrap:wrap;gap:10px;padding:12px}.filters label{color:var(--muted);display:grid;font-size:11px;gap:4px;min-width:125px}.filters input,.filters select,.issue-form input,.issue-form textarea,.comment-form textarea,.inline-form input{background:#fff;border:1px solid #cdd6e0;border-radius:7px;color:var(--ink);padding:8px 9px}.filters .button{margin-left:auto}.table-wrap{background:#fff;border:1px solid var(--line);border-radius:10px;margin-top:12px;overflow:auto}.issue-table{border-collapse:collapse;min-width:760px;width:100%}.issue-table th,.issue-table td{border-bottom:1px solid #edf0f3;padding:12px 14px;text-align:left;vertical-align:top}.issue-table th{color:var(--muted);font-size:11px;text-transform:uppercase;letter-spacing:.08em}.issue-table tr:last-child td{border-bottom:0}.issue-table a{text-decoration:none}.status-pill{border-radius:999px;display:inline-block;font-size:11px;font-weight:700;padding:4px 8px}.status-unclaimed{background:#eef1f5;color:#5a6674}.status-in-progress{background:#e8eeff;color:#3459be}.status-blocked{background:#fff0ed;color:#b94a3d}.status-done{background:#e7f6ee;color:#207447}.detail-shell{max-width:1180px}.detail-grid{align-items:start;display:grid;gap:18px;grid-template-columns:minmax(0,1fr) 340px}.detail-main,.detail-sidebar{display:grid;gap:18px}.panel{background:#fff;border:1px solid var(--line);border-radius:12px;box-shadow:var(--shadow);padding:20px}.panel-heading{align-items:start;display:flex;justify-content:space-between;gap:16px;margin-bottom:14px}.panel h2{font-size:1.08rem;margin:3px 0}.panel h3{font-size:.85rem;margin:18px 0 8px}.markdown-body,.comment pre{background:#f6f8fa;border:1px solid #edf0f3;border-radius:8px;font:13px/1.6 ui-monospace,SFMono-Regular,Consolas,monospace;margin:0;padding:14px;white-space:pre-wrap;word-break:break-word}.issue-form{display:grid;gap:11px;margin-top:18px}.issue-form label{color:var(--muted);display:grid;font-size:12px;gap:5px}.form-message{color:#257048;font-size:12px;margin-left:8px}.comment-list{display:grid;gap:11px}.comment{border-top:1px solid #edf0f3;padding-top:11px}.comment:first-child{border-top:0;padding-top:0}.comment-meta{align-items:center;color:var(--muted);display:flex;font-size:12px;justify-content:space-between;margin-bottom:6px}.comment-meta strong{color:var(--ink)}.comment pre{font-family:inherit}.comment-form{display:grid;gap:9px;margin-top:18px}.relationship-row{align-items:baseline;display:flex;gap:14px;justify-content:space-between}.link-list{list-style:none;margin:0;padding:0}.link-list li{align-items:center;border-top:1px solid #edf0f3;display:flex;gap:8px;justify-content:space-between;padding:9px 0}.link-list li:first-child{border-top:0}.link-list a{text-decoration:none}.icon-button{background:transparent;border:0;color:#a34f46;font-size:19px;line-height:1}.inline-form{display:flex;gap:7px;margin-top:15px}.inline-form input{min-width:0;width:100%}.empty-state{color:var(--muted);font-size:13px}.metadata-panel{position:sticky;top:16px}@media(max-width:1000px){.lane-columns{grid-template-columns:repeat(4,250px)}.detail-grid{grid-template-columns:1fr}.metadata-panel{position:static}}@media(max-width:620px){.app-shell{padding:18px 14px 40px}.topbar{align-items:stretch;flex-direction:column}.section-heading{align-items:start;flex-direction:column;gap:3px}.filters .button{margin-left:0}.inline-form{flex-wrap:wrap}}
</style>`

const browserBoardScript = `<script>
(function(){
const body=document.body,slug=body.dataset.projectSlug;
const api=async(path,options)=>{const response=await fetch('/api/v1'+path,Object.assign({credentials:'same-origin',headers:{'Content-Type':'application/json'}},options||{}));let payload={};try{payload=await response.json()}catch(_){ }if(!response.ok){throw new Error((payload.error&&payload.error.message)||'Request failed')}return payload};
const escapeHTML=value=>String(value??'').replace(/[&<>"']/g,character=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[character]));
const issueURL=issue=>'/projects/'+encodeURIComponent(issue.project_slug||slug)+'/issues/'+issue.number;
const column=issue=>issue.state==='closed'?'done':issue.blocked?'blocked':(issue.assignee||issue.assigned_to)?'in-progress':'unclaimed';
const title=state=>({'in-progress':'In progress',unclaimed:'Unclaimed',blocked:'Blocked',done:'Done'}[state]||state);
document.querySelectorAll('[data-issue-card]').forEach(card=>card.addEventListener('dragstart',event=>{event.dataTransfer.effectAllowed='move';event.dataTransfer.setData('text/plain',card.dataset.issueId);card.classList.add('dragging')}));
document.addEventListener('dragend',event=>{if(event.target.matches('[data-issue-card]'))event.target.classList.remove('dragging')});
document.querySelectorAll('[data-board-column]').forEach(columnEl=>{columnEl.addEventListener('dragover',event=>{event.preventDefault();event.dataTransfer.dropEffect='move'});columnEl.addEventListener('drop',async event=>{event.preventDefault();const card=document.querySelector('.issue-card.dragging');if(!card)return;columnEl.querySelector('.card-stack').appendChild(card);try{const lane=columnEl.closest('[data-board-lane]');const cards=[...lane.querySelectorAll('[data-issue-card]')];for(let index=0;index<cards.length;index++){await api('/issues/'+encodeURIComponent(cards[index].dataset.issueId)+'/position',{method:'POST',body:JSON.stringify({position:index})})}window.location.reload()}catch(error){window.alert(error.message)}})});
const filters=document.querySelector('[data-issue-filters]');
const renderRows=issues=>{const target=document.querySelector('[data-issue-list-body]');target.innerHTML=issues.map(issue=>{const state=column(issue);const labels=(issue.labels||[]).map(label=>'<span class="chip subtle">'+escapeHTML(label)+'</span>').join('');return '<tr><td><a href="'+issueURL(issue)+'"><strong>#'+issue.number+'</strong> '+escapeHTML(issue.title)+'</a><div class="chips">'+labels+'</div></td><td><span class="status-pill status-'+state+'">'+title(state)+'</span></td><td>'+(issue.assignee?'@'+escapeHTML(issue.assignee):'<span class="muted">Unassigned</span>')+'</td><td>'+(issue.parent?'<a href="'+issueURL(issue.parent)+'">#'+issue.parent.number+' '+escapeHTML(issue.parent.title)+'</a>':'<span class="muted">—</span>')+'</td><td><time datetime="'+escapeHTML(issue.updated_at)+'">'+escapeHTML(new Date(issue.updated_at).toLocaleString())+'</time></td></tr>'}).join('')||'<tr><td colspan="5" class="muted">No matching issues.</td></tr>'};
const loadIssues=async()=>{const query=new URLSearchParams();const values=[['q','[data-filter-search]'],['state','[data-filter-state]'],['assigned','[data-filter-assigned]'],['parent','[data-filter-parent]'],['assignee','[data-filter-assignee]'],['label','[data-filter-label]']];values.forEach(([name,selector])=>{const control=document.querySelector(selector);if(control&&control.value)query.set(name,control.value)});try{const result=await api('/projects/'+encodeURIComponent(slug)+'/issues?'+query.toString());renderRows(result.issues||[])}catch(error){window.alert(error.message)}};
if(filters)filters.addEventListener('submit',event=>{event.preventDefault();loadIssues()});
const clear=document.querySelector('[data-clear-filters]');if(clear)clear.addEventListener('click',()=>{filters.reset();loadIssues()});
})();
</script>`

const browserDetailScript = `<script>
(function(){
const body=document.body,id=body.dataset.issueId,slug=body.dataset.projectSlug,api=async(path,options)=>{const response=await fetch('/api/v1'+path,Object.assign({credentials:'same-origin',headers:{'Content-Type':'application/json'}},options||{}));let payload={};try{payload=await response.json()}catch(_){ }if(!response.ok)throw new Error((payload.error&&payload.error.message)||'Request failed');return payload};
const message=(selector,text,failed)=>{const element=document.querySelector(selector);if(element){element.textContent=text;element.style.color=failed?'#a34f46':''}};
const form=document.querySelector('[data-issue-form]');if(form)form.addEventListener('submit',async event=>{event.preventDefault();const data=new FormData(form);try{await api('/issues/'+encodeURIComponent(id),{method:'PATCH',body:JSON.stringify({title:data.get('title'),body:data.get('body')})});message('[data-form-message]','Saved');document.querySelector('#issue-markdown').textContent=data.get('body')}catch(error){message('[data-form-message]',error.message,true)}});
const metadata=document.querySelector('[data-metadata-form]');if(metadata)metadata.addEventListener('submit',async event=>{event.preventDefault();const data=new FormData(metadata);const labels=String(data.get('labels')||'').split(',').map(value=>value.trim()).filter(Boolean);try{await api('/issues/'+encodeURIComponent(id),{method:'PATCH',body:JSON.stringify({assignee:String(data.get('assignee')||''),labels,parent_id:String(data.get('parent_id')||'')})});message('[data-metadata-message]','Saved');window.setTimeout(()=>window.location.reload(),250)}catch(error){message('[data-metadata-message]',error.message,true)}});
const toggle=document.querySelector('[data-toggle-state]');if(toggle)toggle.addEventListener('click',async()=>{const next=toggle.textContent.toLowerCase().includes('reopen')?'reopen':'close';try{await api('/issues/'+encodeURIComponent(id)+'/'+next,{method:'POST'});window.location.reload()}catch(error){window.alert(error.message)}});
const comments=document.querySelector('[data-comment-form]');if(comments)comments.addEventListener('submit',async event=>{event.preventDefault();const data=new FormData(comments);try{await api('/issues/'+encodeURIComponent(id)+'/comments',{method:'POST',body:JSON.stringify({body:data.get('body')})});window.location.reload()}catch(error){message('[data-comment-message]',error.message,true)}});
const blockerForm=document.querySelector('[data-blocker-form]');if(blockerForm)blockerForm.addEventListener('submit',async event=>{event.preventDefault();const data=new FormData(blockerForm);try{await api('/issues/'+encodeURIComponent(id)+'/blockers',{method:'POST',body:JSON.stringify({blocker_id:data.get('blocker_id')})});window.location.reload()}catch(error){message('[data-blocker-message]',error.message,true)}});
document.querySelectorAll('[data-remove-blocker]').forEach(button=>button.addEventListener('click',async()=>{try{await api('/issues/'+encodeURIComponent(id)+'/blockers/'+encodeURIComponent(button.dataset.removeBlocker),{method:'DELETE'});window.location.reload()}catch(error){window.alert(error.message)}}));
})();
</script>`
