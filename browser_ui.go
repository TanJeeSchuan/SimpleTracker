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
	b.WriteString(`<section class="board-section" aria-labelledby="board-title"><div class="section-heading"><div><p class="eyebrow">Primary work surface</p><h2 id="board-title">Swimlane board</h2></div><div><span class="legend">Unclaimed · In progress · Blocked · Done</span><span class="form-message" role="status" data-board-message></span></div></div><div class="board" data-board>`)
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
	b.WriteString(`</select></label><label>Assignee<input type="search" name="assignee" data-filter-assignee placeholder="agent or person"></label><label>Label<input type="search" name="label" data-filter-label placeholder="label"></label><label>Updated since<input type="datetime-local" name="updated_since" data-filter-updated-since></label><label>Updated until<input type="datetime-local" name="updated_until" data-filter-updated-until></label><label>Age<input type="text" name="age" data-filter-age placeholder="days (7) or 168h"></label><button class="button" type="submit">Apply filters</button><button class="button ghost" type="button" data-clear-filters>Clear</button></form><div class="table-wrap"><table class="issue-table"><thead><tr><th>Issue</th><th>State</th><th>Assignment</th><th>Parent</th><th>Updated</th></tr></thead><tbody data-issue-list-body>`)
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
