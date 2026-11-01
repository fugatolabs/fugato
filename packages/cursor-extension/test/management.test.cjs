const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const vm=require('node:vm');

function renderer() {
  const root={addEventListener(){}};
  const context={document:{body:{dataset:{surface:'management'}},getElementById:()=>root,addEventListener(){}},window:{addEventListener(){}},
    acquireVsCodeApi:()=>({getState:()=>({}),postMessage(){}})};
  vm.createContext(context);
  // Expose the existing renderer inside the test VM without adding a page API.
  const script=fs.readFileSync(path.join(__dirname,'../resources/sidebar.js'),'utf8');
  vm.runInContext(script.replace("vscode.postMessage({type:'ready'});", "globalThis.renderForTest = rows => { model = {logs:undefined}; return details(rows); };"),context);
  return context.renderForTest;
}

const row=(label,description='',action,children)=>({id:label,label,description,action,children,collapsed:true});
const worktree=()=>[row('Branch','feature'),row('Worktree path','/repo'),row('Worktree actions','',undefined,[row('Open folder','','open'),{...row('Remove worktree','','remove'),context:'destructive'}])];
const configuration=()=>row('Configuration & setup','',undefined,[row('Project path','/repo/app'),row('Configuration','.fugato.yaml'),row('Edit configuration','','edit'),row('Validate configuration','','doctor')]);

test('management has one environment context and visible destructive actions',()=>{
  const render=renderer();
  const html=render([...worktree(),{...row('app','abc'),id:'selected-environment:app'},row('Runtime','stopped'),row('Start','','start'),row('Project checks','',undefined,[row('ready','Requires a running environment.')]),configuration(),
    row('More actions','',undefined,[row('Run command','','run'),row('Latest verification','','latest'),{...row('Destroy environment','','destroy'),context:'destructive'}])]);
  const toolbar=html.slice(html.indexOf('data-key="main-actions"'),html.indexOf('class="tabs"'));
  assert.match(toolbar,/>Start<\/button>/);
  assert.doesNotMatch(toolbar,/View logs|Configuration|Run check|Add environment|Create environment/);
  assert.equal((html.match(/data-tab="logs"/g)||[]).length,1,'Logs has one navigation control');
  assert.equal((html.match(/data-tab="configuration"/g)||[]).length,1,'Configuration has one navigation control');
  assert.doesNotMatch(html,/Add environment|class="overflow"|More environment actions|More worktree actions/);
  assert.match(toolbar,/Destroy environment<\/button>/,'Destruction has a short explicit label');
  assert.match(html,/Remove worktree<\/button>/,'Worktree removal has a short explicit label');
  assert.match(html,/Start to view services\./);
  assert.ok(html.indexOf('Run check')>html.indexOf('id="panel-overview"'),'Checks belong to Overview');
  assert.match(html,/Verification<\/summary>/);
  assert.doesNotMatch(html,/View ports/);
});

test('named checks explain disabled prerequisites and show their own results',()=>{
  const html=renderer()([...worktree(),{...row('app','abc'),id:'selected-environment:app'},row('Runtime','stopped'),row('Start','','start'),
    row('Project checks','',undefined,[{...row('ready','Requires a running environment.'),id:'check:/repo with spaces:ready'},row('unit-tests','Passed','unit'),row('integration','Failed: HTTP 500','integration')])]);
  assert.match(html,/disabled aria-label="Run check ready" aria-describedby="check-hint-check%3A%2Frepo%20with%20spaces%3Aready"/);
  assert.match(html,/Requires a running environment\./);
  assert.match(html,/data-action="unit"/,'Independent checks remain actionable');
  assert.equal((html.match(/>Run again<\/button>/g)||[]).length,2);
  assert.match(html,/Failed: HTTP 500/);
});

test('running toolbar keeps lifecycle actions and leaves preview with its service',()=>{
  const html=renderer()([...worktree(),{...row('app','abc'),id:'selected-environment:app'},row('Runtime','running'),row('Stop','','stop'),row('Restart','','restart'),
    row('Services & ports','',undefined,[row('web','running',undefined,[row('http://web.localhost/','3000/http','preview')])])]);
  const toolbar=html.slice(html.indexOf('data-key="main-actions"'),html.indexOf('class="tabs"'));
  assert.match(toolbar,/>Stop<\/button>/);assert.match(toolbar,/>Restart<\/button>/);
  assert.doesNotMatch(toolbar,/preview|http:/);
  assert.match(html,/data-action="preview"/);
});

test('empty worktrees offer creation without stopped-environment controls',()=>{
  const html=renderer()([...worktree(),row('Create environment','','create')]);
  assert.match(html,/No environment in this worktree/);
  assert.equal((html.match(/>Create environment<\/button>/g)||[]).length,1);
  assert.doesNotMatch(html,/role="tab"|>Start<\/button>|This environment is created and stopped/);
});

test('setup has an escape action and creates after reviewing configuration',()=>{
  const html=renderer()([...worktree(),row('Create environment setup'),row('Choose directory…','','directory'),row('Configured projects','',undefined,[row('app','native','select')]),configuration(),row('Create','','create'),row('Cancel','','cancel')]);
  assert.match(html,/>Cancel<\/button>/);
  assert.ok(html.indexOf('>Create</button>')>html.indexOf('>Edit configuration</button>'),'Review precedes the Create action');
  assert.doesNotMatch(html,/role="tab"|>Start<\/button>/);
});
