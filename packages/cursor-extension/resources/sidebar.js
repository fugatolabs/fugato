(() => {
  'use strict';
  const vscode = acquireVsCodeApi();
  const root = document.getElementById('fugato');
  const saved = vscode.getState() || {};
  const disclosure = saved.disclosure || {};
  let selectedTab = ['overview','logs','configuration'].includes(saved.tab) ? saved.tab : 'overview';
  const management = document.body.dataset.surface === 'management';
  let logService = '', environmentKey = '';
  const pending = new Set();
  let revision = '', model;
  const previous = { repositories: '', details: '', connection: '' };
  const escape = value => String(value || '').replace(/[&<>"']/g, char => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'})[char]);
  const icons = {
    repo:'<path d="M3 4h5l2 2h8v13H3z"/><path d="M8 12h5m-2-2 2 2-2 2"/>',
    box:'<path d="m12 3 9 5v9l-9 5-9-5V8zM3 8l9 5 9-5M12 13v9M7 5.2l9 5"/>',
    branch:'<circle cx="6" cy="5" r="3"/><circle cx="18" cy="5" r="3"/><circle cx="6" cy="19" r="3"/><path d="M6 8v8M18 8c0 6-12 1-12 8"/>',
    chevron:'<path d="m9 5 7 7-7 7"/>',
    copy:'<rect x="8" y="8" width="12" height="13" rx="2"/><path d="M16 8V3H3v13h5"/>',
    trash:'<path d="M3 6h18M9 6V3h6v3M5 6l1 15h12l1-15M10 10v7M14 10v7"/>',
    server:'<rect x="3" y="4" width="18" height="7" rx="2"/><rect x="3" y="13" width="18" height="7" rx="2"/><path d="M7 7h.01M7 16h.01"/>'
  };
  const icon = name => `<span class="icon" aria-hidden="true"><svg viewBox="0 0 24 24">${icons[name] || icons.box}</svg></span>`;
  const key = value => `data-key="${escape(value)}"`;
  const action = value => value ? `data-action="${escape(value)}"${pending.has(value) ? ' disabled' : ''}` : 'disabled';
  const label = state => ({running:'Running',stopped:'Stopped',error:'Error','not started':'Not started'})[state.toLowerCase()] || state;
  function status(state) { return `<span class="status ${escape(state.toLowerCase())}"><span class="status-mark" aria-hidden="true"></span>${escape(label(state))}</span>`; }
  function button(row, primary = false) { return `<button type="button" ${key(row.action)} class="button${primary ? ' primary' : ''}" ${action(row.action)}>${escape(row.label)}</button>`; }
  function textButton(row) { return `<button type="button" ${key(row.action)} class="text-button${row.context === 'destructive' ? ' destructive' : ''}" ${action(row.action)}>${row.context === 'destructive' ? icon('trash') : ''}${escape(row.label)}</button>`; }
  function opened(id, fallback) { return typeof disclosure[id] === 'boolean' ? disclosure[id] : fallback; }
  function hasSelection(row) { return row.selected || row.children?.some(hasSelection); }
  function worktree(row) {
    const open = opened(row.id, hasSelection(row) || row.description.includes('Current folder'));
    return `<div class="worktree" ${key(row.id)}><div class="worktree-heading">${row.children?.length ? `<button class="worktree-toggle" type="button" data-toggle="${escape(row.id)}" aria-label="${open ? 'Collapse' : 'Expand'} ${escape(row.label)}" aria-expanded="${open}">${icon('chevron')}</button>` : icon('branch')}<button type="button" class="branch-button" ${action(row.action)} aria-pressed="${row.selected}" title="${escape(row.tooltip)}"><span class="branch-name">${escape(row.label)}</span><span class="branch-context">${escape(row.description)}</span></button></div>${row.children?.length ? `<div class="worktree-environments" data-disclosure="${escape(row.id)}"${open ? '' : ' hidden'}>${row.children.map(environment).join('')}</div>` : ''}</div>`;
  }
  function environment(row) {
    const parts = row.description.split(' · '), id = parts.shift(), state = parts.join(' · ');
    return `<button type="button" class="environment" ${key(row.id)} ${action(row.action)} aria-pressed="${row.selected}" title="${escape(row.tooltip)}">${icon('box')}<span class="environment-text"><span class="environment-name">${escape(row.label)}</span><span class="environment-id">${escape(id)}</span></span>${status(state || 'Not started')}</button>`;
  }
  function repositories(rows) {
    return rows.map(repo => `<div class="repository" ${key(repo.id)}><div class="repo-heading">${icon('repo')}<span class="repo-name">${escape(repo.label)}</span></div>${(repo.children || []).map(row => row.id.startsWith('missing:') ? `<details class="missing" ${key(row.id)} ${opened(row.id, false) ? 'open' : ''}><summary>${escape(row.label)} <span class="muted">${escape(row.description)}</span></summary>${(row.children || []).map(worktree).join('')}</details>` : worktree(row)).join('')}</div>`).join('') || '<p class="note">Open a local Git repository to see its worktrees.</p>';
  }
  function choices(row) {
    return `<details ${key(row.id)} ${opened(row.id, !row.collapsed) ? 'open' : ''}><summary>${row.label === 'Other environments & setup' ? 'Set up another environment…' : escape(row.label)}</summary>${row.children.map(child => `<div class="setup-choice" ${key(child.id)}>${textButton(child)}<span class="muted">${escape(child.description)}</span></div>`).join('')}</details>`;
  }
  function services(row) {
    return `<div class="services" ${key(row.id)}><h3>Services &amp; ports</h3>${row.children.map(service => {
      if (!service.children) { return `<p class="note" ${key(service.id)}>${escape(service.label)}${service.description ? ': ' + escape(service.description) : ''}</p>`; }
      return `<div class="service" ${key(service.id)}><div class="service-heading">${icon('server')}<strong>${escape(service.label)}</strong><span class="muted">${escape(service.description)}</span></div>${service.children.map(port => `<div class="service-port" ${key(port.id)}>${port.label === 'Internal' ? `<span class="internal-port">${escape(port.description)} · Internal only</span>` : `${port.action ? `<button type="button" class="text-button port-address" ${action(port.action)} title="${escape(port.tooltip)}">${escape(port.label)}</button>` : `<span class="port-address">${escape(port.label)}</span>`}<span class="port-meta muted">${escape(port.description)}</span>${port.copyAction ? `<button type="button" class="icon-button" ${action(port.copyAction)} aria-label="Copy ${escape(service.label)} address">${icon('copy')}</button>` : ''}`}</div>`).join('')}</div>`;
    }).join('')}</div>`;
  }
  function projectChecks(group) {
    return `<section class="overview-section" aria-label="Project checks" ${key(group.id)}><h3>Project checks</h3>${group.children.map(row => {
      const hintId = escape('check-hint-' + encodeURIComponent(row.id));
      return `<div class="project-check" ${key(row.id)}><div class="check-description"><strong>${escape(row.label)}</strong><p class="note" id="${hintId}">${escape(row.description || 'Not run')}</p></div><button type="button" class="button" ${action(row.action)} aria-label="Run check ${escape(row.label)}" aria-describedby="${hintId}">${/^(Passed|Failed)/.test(row.description) ? 'Run again' : 'Run check'}</button></div>`;
    }).join('')}</section>`;
  }
  function details(rows) {
    const heading = rows.find(row => row.id.startsWith('selected-environment:'));
    const branch = rows.find(row => row.label === 'Branch');
    const runtime = rows.find(row => row.label === 'Runtime');
    const serviceRows = rows.find(row => row.label === 'Services & ports');
    const config = rows.find(row => row.label === 'Configuration & setup');
    const more = rows.find(row => row.label === 'More actions');
    const actions = rows.filter(row => row.action && !row.children);
    const primary = actions.find(row => ['Start','Create','Stop'].includes(row.label)) || (!heading ? actions.find(row => row.label.startsWith('Create environment')) : undefined);
    const main = actions.filter(row => row !== primary && ['Stop','Stop (keep data)','Restart'].includes(row.label));
    const checkGroup = rows.find(row => row.label === 'Project checks');
    const tasks = (more?.children || []).filter(row => row.label === 'Run command');
    const verification = (more?.children || []).filter(row => ['Verify HTTP','Verify browser','Latest verification','Open screenshot'].includes(row.label));
    const destructive = [...actions, ...(more?.children || [])].filter(row => row.context === 'destructive');
    const output = [];
    const creating = rows.some(row => row.label === 'Create environment setup');
    const worktreePath = rows.find(row => row.label === 'Worktree path');
    const worktreeActions = rows.find(row => row.label === 'Worktree actions');
    const locationLabel = branch?.context === 'primaryWorktree' ? 'Repository' : 'Worktree';
    if (branch) output.push(`<header class="management-header" ${key(branch.id)}><div class="worktree-identity"><span class="eyebrow">${locationLabel}</span><h1>${icon('branch')}${escape(branch.description)}</h1><p class="muted">${escape(worktreePath?.description)}</p></div><div class="header-controls"><div class="header-actions">${worktreeActions?.children?.map(row => row.context === 'destructive' ? textButton(row) : button(row)).join('') || ''}</div>${worktreeActions?.children?.filter(row => row.context === 'destructive' && row.description).map(row => `<p class="note action-hint">${escape(row.description)}</p>`).join('') || ''}</div></header>`);
    if (heading) output.push(`<div class="selection-title" ${key(heading.id)}><span class="selection-name">${escape(heading.label)}</span><span class="environment-id">${escape(heading.description)}</span>${status(runtime?.description || 'Not started')}</div>`);
    const environments = rows.find(row => row.label === 'Choose environment');
    if (environments) output.push(`<label class="environment-selector">Environment <select id="environment-selector" aria-label="Select environment">${environments.children.map(row => `<option value="${escape(row.action)}"${row.selected ? ' selected' : ''}>${escape(row.label)} · ${escape(row.description)}</option>`).join('')}</select></label>`);
    output.push(...rows.filter(row => ['Runtime error','Configuration error'].includes(row.label)).map(row => `<p class="diagnostic" role="status" ${key(row.id)}>${escape(row.description)}</p>`));
    if (creating) output.push('<h2>Create environment</h2><p class="note">Choose a project, review its configuration, then create. Workloads remain stopped.</p>');
    if (creating) {
      const directory = actions.find(row => row.label === 'Choose directory…');
      if (directory) output.push(`<div class="setup-directory">${textButton(directory)}</div>`);
      output.push(...rows.filter(row => ['Configured projects','Advanced: manifest candidates'].includes(row.label)).map(choices));
      if (config) output.push(configuration(config));
      const cancel = actions.find(row => row.label === 'Cancel');
      output.push(`<div class="actions" data-key="setup-actions">${primary ? button(primary, true) : ''}${cancel ? button(cancel) : ''}</div>`);
    } else if (heading) {
      if (primary || main.length || destructive.length) output.push(`<div class="actions" data-key="main-actions">${primary ? button(primary, true) : ''}${main.map(row => button(row)).join('')}<div class="destructive-actions">${destructive.map(textButton).join('')}</div></div>`);
      output.push(`<div class="tabs" role="tablist" aria-label="Environment details">${['overview','logs','configuration'].map(tab => `<button type="button" id="tab-${tab}" role="tab" data-tab="${tab}" aria-selected="${selectedTab === tab}" aria-controls="panel-${tab}" tabindex="${selectedTab === tab ? 0 : -1}">${tab[0].toUpperCase() + tab.slice(1)}</button>`).join('')}</div>`);
      output.push(`<section id="panel-overview" role="tabpanel" aria-labelledby="tab-overview"${selectedTab !== 'overview' ? ' hidden' : ''}>${serviceRows ? services(serviceRows) : `<p class="note">${runtime?.description === 'stopped' ? 'Start to view services.' : 'Services are available while running.'}</p>`}${checkGroup ? projectChecks(checkGroup) : ''}${tasks.length ? `<section class="overview-section" aria-label="Tasks"><h3>Tasks</h3><div class="secondary-actions">${tasks.map(textButton).join('')}</div></section>` : ''}${verification.length ? choices({...more, label:'Verification', children:verification}) : ''}</section>`);
      const logServices = rows.find(row => row.label === 'Log services');
      output.push(`<section id="panel-logs" role="tabpanel" aria-labelledby="tab-logs"${selectedTab !== 'logs' ? ' hidden' : ''}>${logServices ? `<div class="log-toolbar"><label>Service <select id="log-service" aria-label="Log service">${logServices.children.map(row => `<option value="${escape(row.action)}"${logService === row.action ? ' selected' : ''}>${escape(row.label)}${row.description ? ' · ' + escape(row.description) : ''}</option>`).join('')}</select></label><button type="button" id="refresh-logs" class="button">Refresh logs</button></div>${logServices.description ? `<p class="note">${escape(logServices.description)}</p>` : ''}` : ''}<pre class="log-output" tabindex="0" aria-label="Service log output">${escape(model.logs === undefined ? 'Choose a service or refresh to load recent logs.' : model.logs || 'No recent logs.')}</pre></section>`);
      output.push(`<section id="panel-configuration" role="tabpanel" aria-labelledby="tab-configuration"${selectedTab !== 'configuration' ? ' hidden' : ''}>${config ? configuration(config) : '<p class="note">Configuration unavailable. Restore the worktree folder to edit configuration.</p>'}</section>`);
    } else if (branch) {
      const create = actions.find(row => row.label === 'Create environment');
      output.push(`<section class="empty-state" aria-label="No environment"><h2>No environment in this ${locationLabel.toLowerCase()}</h2><p class="note">Choose a project and create its environment before starting workloads.</p>${create ? button(create, true) : ''}</section>`);
    }
    output.push(...rows.filter(row => !row.action && !row.children && row !== heading && row !== branch && row !== runtime && !['Runtime error','Configuration error','Worktree path','Create environment setup'].includes(row.label)).map(row => `<p class="note" ${key(row.id)}>${escape(row.label)}${row.description ? ': ' + escape(row.description) : ''}</p>`));
    if (!rows.length) output.push('<p class="muted">Select a worktree in the Fugato sidebar.</p>');
    return output.join('');
  }
  function configuration(config) { return `<div class="configuration" ${key(config.id)}><dl>${config.children.filter(row => !row.action).map(row => `<dt ${key(row.id + ':label')}>${escape(row.label)}</dt><dd ${key(row.id)}>${escape(row.description)}</dd>`).join('')}</dl><div class="secondary-actions">${config.children.filter(row => row.action).map(textButton).join('')}</div></div>`; }
  // Patch keyed nodes in place: preserve focus, scroll, disclosure and text selection.
  function reconcile(parent, incoming) {
    let cursor = parent.firstChild;
    for (const fresh of [...incoming.childNodes]) {
      const id = fresh.nodeType === 1 && fresh.getAttribute('data-key');
      let current = id ? [...parent.childNodes].find(node => node.nodeType === 1 && node.getAttribute('data-key') === id) : cursor;
      if (!current || current.nodeType !== fresh.nodeType || current.nodeName !== fresh.nodeName || (!id && current.nodeType === 1 && current.hasAttribute('data-key'))) {
        current = fresh.cloneNode(true); parent.insertBefore(current, cursor);
      } else {
        if (current !== cursor) parent.insertBefore(current, cursor);
        if (current.nodeType === 3) { if (current.nodeValue !== fresh.nodeValue) current.nodeValue = fresh.nodeValue; }
        else if (current.nodeType === 1) {
          for (const attr of [...current.attributes]) if (!fresh.hasAttribute(attr.name)) current.removeAttribute(attr.name);
          for (const attr of fresh.attributes) if (current.getAttribute(attr.name) !== attr.value) current.setAttribute(attr.name, attr.value);
          reconcile(current, fresh);
        }
      }
      cursor = current.nextSibling;
    }
    while (cursor) { const next = cursor.nextSibling; parent.removeChild(cursor); cursor = next; }
  }
  function patch(id, markup) { const template = document.createElement('template'); template.innerHTML = markup; reconcile(document.getElementById(id), template.content); }
  function render() {
    if (!model) return;
    const repos = JSON.stringify(model.repositories), selected = JSON.stringify([model.details, model.logs, selectedTab]);
    if (!management && repos !== previous.repositories) { patch('repositories', repositories(model.repositories)); previous.repositories = repos; }
    if (management && selected !== previous.details) { patch('details', details(model.details)); previous.details = selected; }
    const logSelector = document.getElementById('log-service');
    if (logSelector) { logSelector.value = logService || logSelector.options[0]?.value || ''; }
    const environmentSelector = document.getElementById('environment-selector');
    if (environmentSelector) { environmentSelector.value = model.details.find(row => row.label === 'Choose environment')?.children?.find(row => row.selected)?.action || environmentSelector.options[0]?.value || ''; }
    const connection = JSON.stringify([model.connection, model.diagnostic, model.stale]);
    if (connection !== previous.connection) {
      patch('connection', `${model.connection ? `<p class="${model.stale ? 'diagnostic' : 'note'}">${escape(model.connection)}</p>` : ''}${model.stale ? `<div class="connection-actions">${textButton({label:'Start daemon',action:model.startDaemon})}${textButton({label:'Setup instructions',action:model.setup})}</div>` : ''}`);
      document.getElementById('daemon-state').textContent = model.stale ? 'Local daemon unavailable' : model.connection?.startsWith('Connecting') ? 'Connecting to local daemon…' : 'Local daemon connected';
      previous.connection = connection;
    }
    let diagnostic = document.getElementById('selection-diagnostic');
    if (!diagnostic) { diagnostic = document.createElement('p'); diagnostic.id='selection-diagnostic'; diagnostic.className='diagnostic'; document.getElementById('selection').appendChild(diagnostic); }
    diagnostic.textContent = model.diagnostic || ''; diagnostic.hidden = !model.diagnostic;
    root.querySelectorAll('button[data-action]').forEach(button => { button.disabled = pending.has(button.dataset.action); button.setAttribute('aria-busy', String(button.disabled)); });
    for (const [id, capability] of [['refresh', model.refresh], ['create-worktree', model.createWorktree]]) {
      const control = document.getElementById(id); control.disabled = !capability || pending.has(capability); control.setAttribute('aria-busy', String(!!capability && pending.has(capability)));
    }
    const refreshLogs = document.getElementById('refresh-logs');
    if (refreshLogs) refreshLogs.disabled = !logSelector?.value || pending.has(logSelector.value);
  }
  function dispatch(id) {
    if (!id || pending.has(id)) return;
    pending.add(id); render(); vscode.postMessage({type:'action',action:id,revision});
  }
  function tab(name) {
    if (!['overview','logs','configuration'].includes(name)) return;
    selectedTab = name; vscode.setState({disclosure,tab:selectedTab}); render();
    if (name === 'logs' && model.logs === undefined) dispatch(document.getElementById('log-service')?.value);
  }
  root.addEventListener('change', event => {
    if (event.target.id === 'log-service') { logService = event.target.value; dispatch(logService); }
    else if (event.target.id === 'environment-selector') dispatch(event.target.value);
  });
  root.addEventListener('keydown', event => {
    if (event.target.getAttribute('role') !== 'tab' || !['ArrowLeft','ArrowRight','Home','End'].includes(event.key)) return;
    const names = ['overview','logs','configuration'], index = names.indexOf(selectedTab);
    const next = event.key === 'Home' ? names[0] : event.key === 'End' ? names[2] : names[(index + (event.key === 'ArrowRight' ? 1 : 2)) % 3];
    event.preventDefault(); tab(next); document.getElementById(`tab-${next}`)?.focus();
  });
  root.addEventListener('click', event => {
    const tabButton = event.target.closest('button[data-tab]');
    if (tabButton) { tab(tabButton.dataset.tab); return; }
    if (event.target.closest('#refresh-logs')) { dispatch(document.getElementById('log-service')?.value); return; }
    const toggle = event.target.closest('button[data-toggle]');
    if (toggle) {
      const open = toggle.getAttribute('aria-expanded') !== 'true'; disclosure[toggle.dataset.toggle] = open;
      toggle.setAttribute('aria-expanded', String(open));
      toggle.setAttribute('aria-label', toggle.getAttribute('aria-label').replace(/^(Expand|Collapse)/, open ? 'Collapse' : 'Expand'));
      const region = [...root.querySelectorAll('[data-disclosure]')].find(node => node.dataset.disclosure === toggle.dataset.toggle);
      if (region) region.hidden = !open;
      vscode.setState({disclosure,tab:selectedTab}); return;
    }
    const button = event.target.closest('button[data-action]');
    const id = button?.dataset.action;
    dispatch(id);
  });
  root.addEventListener('toggle', event => {
    if (event.target.tagName !== 'DETAILS') return;
    disclosure[event.target.dataset.key] = event.target.open; vscode.setState({disclosure,tab:selectedTab});
  }, true);
  document.getElementById('refresh').addEventListener('click', () => { if (model) dispatch(model.refresh); });
  document.getElementById('create-worktree').addEventListener('click', () => { if (model) dispatch(model.createWorktree); });
  window.addEventListener('message', event => {
    const message = event.data;
    if (message.type === 'state') {
      model = message.model; revision = message.revision;
      const next = model.details.find(row => row.id.startsWith('selected-environment:'))?.id || '', changed = next !== environmentKey;
      if (changed) { environmentKey = next; logService = ''; }
      render();
      if (management && changed && selectedTab === 'logs' && model.logs === undefined) dispatch(document.getElementById('log-service')?.value);
    }
    else if (message.type === 'selectTab') { tab(message.tab); }
    else if (message.type === 'actionFinished') { pending.delete(message.action); render(); }
    else if (message.type === 'focusSelection') { document.getElementById('selection').scrollIntoView({block:'nearest'}); }
    else if (message.type === 'actionError') { const diagnostic = document.getElementById('selection-diagnostic'); if (diagnostic) { diagnostic.textContent = message.message; diagnostic.hidden = false; } }
  });
  vscode.postMessage({type:'ready'});
})();
