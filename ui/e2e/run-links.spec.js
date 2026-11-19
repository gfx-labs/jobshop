import { test, expect } from '@playwright/test';

test('running ticket references remain clickable in desktop and compact layouts', async ({ page, request, baseURL, isMobile }) => {
  test.skip(!process.env.DESK_MUTATE || !process.env.DESK_LINK_WORKFLOW, 'set DESK_MUTATE=1 and DESK_LINK_WORKFLOW to a disposable workflow accepting repo, pr and issue variables');
  const call = async (method, data) => {
    const response = await request.post(new URL('api/rpc/' + method, baseURL).href, { headers: { 'X-Jobshop-Desk': '1' }, data });
    expect(response.ok(), await response.text()).toBeTruthy();
    return response.json();
  };
  const { run } = await call('run.start', { workflow: process.env.DESK_LINK_WORKFLOW, vars: { repo: 'gfx-labs/jobshop', pr: '988', issue: 'JOB-116' } });
  try {
    await call('ticket.update', { id: run.id, meta: { linear: { workspace: 'gfxlabs' } } });
    await page.goto('./');
    if (isMobile) await page.getByRole('navigation', { name: 'Mobile' }).getByRole('button', { name: 'in flight', exact: true }).click();
    const row = page.getByRole('region', { name: 'In flight', exact: true }).getByRole('row').filter({ has: page.locator(`.ticket-label[title*="${run.id}"]`) });
    await expect(row.getByRole('link', { name: 'JOB-116', exact: true })).toHaveAttribute('href', 'https://linear.app/gfxlabs/issue/JOB-116');
    await expect(row.getByRole('link', { name: 'jobshop#988', exact: true })).toHaveAttribute('href', 'https://github.com/gfx-labs/jobshop/pull/988');
    await expect(row.getByRole('link', { name: 'jobshop#988', exact: true })).toBeVisible();
  } finally {
    await call('run.cancel', { id: run.id });
  }
});

test('issue-started run shows no PR, then the PR its step records, without a reload (JOB-131)', async ({ page, request, baseURL, isMobile }) => {
  test.skip(!process.env.DESK_MUTATE || !process.env.DESK_ISSUE_WORKFLOW, 'set DESK_MUTATE=1 and DESK_ISSUE_WORKFLOW to a disposable workflow with an issue variable and an agent step in a pool that spawns nothing');
  const call = async (method, data) => {
    const response = await request.post(new URL('api/rpc/' + method, baseURL).href, { headers: { 'X-Jobshop-Desk': '1' }, data });
    expect(response.ok(), await response.text()).toBeTruthy();
    return response.json();
  };
  const { run, steps } = await call('run.start', { workflow: process.env.DESK_ISSUE_WORKFLOW, vars: { issue: '131' } });
  try {
    await page.goto('./');
    if (isMobile) await page.getByRole('navigation', { name: 'Mobile' }).getByRole('button', { name: 'in flight', exact: true }).click();
    const row = page.getByRole('region', { name: 'In flight', exact: true }).getByRole('row').filter({ has: page.locator(`.ticket-label[title*="${run.id}"]`) });
    await expect(row).toBeVisible();
    await expect(row.locator('a[href*="/pull/"]')).toHaveCount(0);
    if (!isMobile) await expect(row.locator('td.ref-col').nth(1)).toHaveText('–');
    // A mention is not the run's PR, the handoff line is.
    await call('comment.add', { id: steps[0].id, body: 'related: https://github.com/acme/widget/pull/5' });
    await call('comment.add', { id: steps[0].id, body: 'PR https://github.com/acme/widget/pull/131 head abc1234' });
    // The wide layout shows the PR column, the compact one links beneath the title.
    const link = row.locator('a[href="https://github.com/acme/widget/pull/131"]').locator('visible=true');
    await expect(link).toHaveCount(1);
    await expect(link).toHaveText('widget#131');
    await expect(link).toHaveClass(isMobile ? /ref-stack/ : /^$/);
    await expect(row.locator('a[href$="/pull/5"]')).toHaveCount(0);
    // Desktop: labeled issue and pr columns beside the title. Compact: the same
    // links stacked under it. Never both, and no horizontal overflow.
    const headers = page.getByRole('region', { name: 'In flight', exact: true }).getByRole('columnheader');
    if (isMobile) {
      await expect(headers.filter({ hasText: /^issue$/ })).toHaveCount(0);
      await expect(row.locator('td.ref-col').first()).toBeHidden();
    } else {
      await expect(headers.filter({ hasText: /^issue$/ })).toBeVisible();
      await expect(headers.filter({ hasText: /^pr$/ })).toBeVisible();
      await expect(row.locator('.ref-stack').first()).toBeHidden();
    }
    const over = await page.evaluate(() => { const s = document.querySelector('.table-scroll.refs'); return s.scrollWidth - s.clientWidth; });
    expect(over).toBeLessThanOrEqual(0);
  } finally {
    await call('run.cancel', { id: run.id });
  }
});

// A run whose PR links could not be read shows a marker, not the dash that
// means no PR yet (#1294). The states come from a mocked overview: the
// controller reads its own store, so it cannot be made to fail here.
test('in-flight table separates unavailable PR data from no PR yet (#1294)', async ({ page, isMobile }) => {
  const at = '2026-11-19T00:00:00Z';
  const run = (n, title, pr_state, links) => ({
    run: { id: `js-pr-state-${n}`, project: 'js', kind: 'run', title, status: 'open', fields: { state: 'running', vars: { issue: String(n) } }, created_at: at, updated_at: at },
    steps: [],
    links,
    pr_state,
  });
  const runs = [
    run(1, 'Run without a PR', 'none'),
    run(2, 'Run with unreadable PR data', 'unavailable'),
    run(3, 'Run with a PR', 'linked', ['https://github.com/acme/widget/pull/3']),
  ];
  await page.route('**/api/live**', (route) => route.fulfill({
    status: 200,
    headers: { 'content-type': 'text/event-stream', 'cache-control': 'no-store' },
    body: `event: overview\ndata: ${JSON.stringify({ runs, truncated: false })}\n\n`,
  }));
  await page.goto('./');
  if (isMobile) await page.getByRole('navigation', { name: 'Mobile' }).getByRole('button', { name: 'in flight', exact: true }).click();
  const row = (title) => page.getByRole('region', { name: 'In flight', exact: true }).getByRole('row').filter({ hasText: title });
  const marker = (title) => row(title).locator('.pr-unavailable').locator('visible=true');

  await expect(row('Run with a PR').locator('a[href$="/pull/3"]').locator('visible=true')).toHaveText('widget#3');
  await expect(row('Run with a PR').locator('.pr-unavailable')).toHaveCount(0);

  await expect(marker('Run with unreadable PR data')).toHaveCount(1);
  await expect(marker('Run with unreadable PR data')).toHaveText('pr unavailable');
  await expect(marker('Run with unreadable PR data')).toHaveAttribute('title', /could not be read/);
  await expect(row('Run with unreadable PR data').locator('a[href*="/pull/"]')).toHaveCount(0);

  await expect(row('Run without a PR')).toBeVisible();
  await expect(row('Run without a PR').locator('.pr-unavailable')).toHaveCount(0);
  if (!isMobile) await expect(row('Run without a PR').locator('td.ref-col').nth(1)).toHaveText('–');
  else await expect(row('Run without a PR').locator('a[href*="/pull/"]')).toHaveCount(0);
});
