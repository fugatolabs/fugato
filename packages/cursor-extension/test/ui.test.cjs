const {test} = require('node:test');
const assert = require('node:assert/strict');
const Module = require('node:module');
const fs = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
const {Client,APIError} = require('../out/client');
const security = require('../out/security');
const local = require('../out/local');

const disposable = {dispose(){}};
class Emitter {fires=0;event=()=>disposable;fire(){this.fires++;}dispose(){}}
class Item {constructor(label,collapsibleState){this.label=label;this.collapsibleState=collapsibleState;}}
function uri(filename, scheme='file') {return {fsPath:filename,path:filename,scheme,toString(){return `${scheme}:${filename}`;},with(values){return uri(filename,values.scheme||scheme);}};}

test('worktree details, project selection and configuration checks never start workloads', async t => {
  const root = await fs.realpath(await fs.mkdtemp(path.join(os.tmpdir(),'fugato-ui-')));
  t.after(()=>fs.rm(root,{recursive:true,force:true}));
  const repository=path.join(root,'.git');
  const project = name => ({id:name==='web'?'aaaaaaaaaaaaaaaa':'bbbbbbbbbbbbbbbb',project_dir:path.join(root,name),config_file:path.join(root,name,'.fugato.yaml'),compose_files:[],kind:'native',configured:true,commands:[{name:'test',mode:'task'},{name:'repl',mode:'interactive'}],checks:['ready','lint'],check_details:[{name:'ready',requires_running:true,reason:'Requires a running endpoint'},{name:'lint',requires_running:false}],endpoints:[{name:'site',command:'dev',port:0,protocol:'http',browser:true},{name:'admin',command:'admin',port:0,protocol:'http',browser:true}]});
  const projects=[project('web'),project('worker')];
  for(const project of projects){await fs.mkdir(project.project_dir);await fs.writeFile(project.config_file,'version: 1\n');}
  const runtime={id:projects[0].id,repository,worktree:root,project_dir:projects[0].project_dir,commit:'abc',desired:'running',observed:'running'};
  const commands=new Map(),views=new Map(),reports=new Map(),errors=[],errorActions=[],shown=[],external=[],calls=[],terminals=[],edits=[],selectionWrites=[];
  let pick=0,offline=false,change,configurationChange,confirmation,pickedDirectory,managementReceive,onManagementMessage,holdErrorNotification=false,dismissError;
  const confirmations=[],panels=[],managementMessages=[],openedFolders=[];let registered=[runtime];
  const vscode={
    RelativePattern:class{},TreeItem:Item,TreeItemCollapsibleState:{Collapsed:2,Expanded:1,None:0},EventEmitter:Emitter,ThemeIcon:class{},StatusBarAlignment:{Left:1},ViewColumn:{Active:-1},ProgressLocation:{Notification:1},Position:class{},
    WorkspaceEdit:class{insert(uri,position,text){edits.push(text);}},Uri:{file:filename=>uri(filename),joinPath:(base,...parts)=>uri(path.join(base.fsPath,...parts)),parse:value=>({toString:()=>value}),from:value=>uri(value.path,value.scheme)},
    commands:{registerCommand:(name,handler)=>{commands.set(name,handler);return disposable;},executeCommand:async(name,...args)=>name==='vscode.openFolder'?openedFolders.push(args):commands.get(name)?.(...args)},
    env:{clipboard:{writeText:async value=>external.push(value)},openExternal:async value=>{external.push(value.toString());return true;}},
    workspace:{isTrusted:true,workspaceFolders:[{uri:uri(root)}],getConfiguration:()=>({inspect:()=>({globalValue:undefined})}),
      onDidChangeWorkspaceFolders:()=>disposable,onDidChangeConfiguration:handler=>{configurationChange=handler;return disposable;},
      registerTextDocumentContentProvider:(scheme,provider)=>{reports.set(scheme,provider);return disposable;},
      createFileSystemWatcher:()=>({dispose(){},onDidChange:handler=>{change=handler;return disposable;},onDidCreate:()=>disposable,onDidDelete:()=>disposable}),
      openTextDocument:async value=>value,applyEdit:async()=>true},
    window:{state:{focused:false},createWebviewPanel:()=>{const panel={...disposable,visible:true,reveal(){},onDidDispose:()=>disposable,onDidChangeViewState:()=>disposable,webview:{cspSource:'vscode-resource:',asWebviewUri:value=>value,onDidReceiveMessage:handler=>{managementReceive=handler;return disposable;},postMessage:async message=>{managementMessages.push(message);onManagementMessage?.(message);return true;}}};panels.push(panel);return panel;},registerWebviewViewProvider:(_,provider)=>{
      for(const [name,state,rows] of [['fugato.worktrees',provider.connection,provider.tree],['fugato.details',provider.selection,provider.details]]){
        const descriptor=Object.getOwnPropertyDescriptor(Object.getPrototypeOf(state),'message'),messages=[];
        Object.defineProperty(state,'message',{get(){return descriptor.get.call(state);},set(value){messages.push(value);descriptor.set.call(state,value);}});
        views.set(name,{provider:rows,messages,get message(){return state.message;}});
      }
      provider.resolveWebviewView({visible:true,show(){},onDidChangeVisibility:()=>disposable,onDidDispose:()=>disposable,webview:{cspSource:'vscode-resource:',asWebviewUri:value=>value,onDidReceiveMessage:()=>disposable,postMessage:async()=>true}});
      return disposable;
    },
      createStatusBarItem:()=>({...disposable,show(){}}),createOutputChannel:()=>({...disposable,clear(){},append(){},appendLine(){},show(){}}),onDidChangeWindowState:()=>disposable,
      showTextDocument:async value=>{shown.push(value);},showOpenDialog:async()=>pickedDirectory?[uri(pickedDirectory)]:undefined,showInputBox:async()=>'/health',showQuickPick:async choices=>choices[pick],showInformationMessage:async()=>undefined,showErrorMessage:async(message,...actions)=>{errors.push(message);errorActions.push(actions);if(holdErrorNotification)return new Promise(resolve=>{dismissError=resolve;});},
      showWarningMessage:async(...args)=>{confirmations.push(args);return typeof confirmation==='function'?confirmation(...args):confirmation;},
      withProgress:async(_,fn)=>fn({}, {onCancellationRequested:()=>disposable}),createTerminal:options=>{terminals.push(options);return{show(){}};}}
  };
  const originalLoad=Module._load;
  Module._load=function(name,...args){return name==='vscode'?vscode:originalLoad.call(this,name,...args);};
  t.after(()=>{Module._load=originalLoad;});
  const publishedPorts=[{name:'site',service:'',command:'dev',container_port:0,protocol:'http',host_ip:'127.0.0.1',host_port:3001,browser:true,url:'http://site.aaaaaaaaaaaaaaaa.localhost:46656/'},{name:'admin',service:'',command:'admin',container_port:0,protocol:'http',host_ip:'127.0.0.1',host_port:3002,browser:true,url:'http://admin.aaaaaaaaaaaaaaaa.localhost:46656/'}];
  const services=[{name:'dev',kind:'native',state:'running',ports:[publishedPorts[0]]},{name:'admin',kind:'native',state:'running',ports:[publishedPorts[1]]},{name:'db',kind:'compose',state:'running',ports:[{service:'db',container_port:5432,protocol:'tcp',host_ip:'',host_port:0}]},{name:'worker',kind:'native',state:'running',ports:[]},{name:'queue',kind:'native',state:'running',ports:[]}];
  let serviceError;
  const methods={info:async()=>{if(offline)throw Error('daemon unavailable');return {instance_id:'test',features:{service_inventory:true,service_logs:true,create_environment:true,destroy_environment:true,worktree_lifecycle:true}};},list:async()=>registered,worktrees:async()=>({repository,worktrees:[{path:root,branch:'feature',missing:false}],truncated:false}),
    projects:async()=>({worktree:root,projects:[...projects],truncated:false}),resolve:async directory=>({...runtime,id:projects.find(p=>p.project_dir===directory).id,project_dir:directory,registered:directory===runtime.project_dir}),get:async()=>runtime,
    ports:async()=>publishedPorts,
    services:async()=>{if(serviceError)throw Error(serviceError);return services;},
    create:async(project,signal)=>{calls.push(['create',project.id,signal.aborted]);const value={...project,desired:'stopped',observed:'stopped'};registered.push(value);return value;},
    logs:async(id,service,kind)=>{calls.push(['logs',id,service,kind]);return{text:`${service||'all'} output`,truncated:false};},
    destroy:async(id,signal)=>{calls.push(['destroy',id,signal.aborted]);registered=registered.filter(record=>record.id!==id);},
    verify:async(id,kind,path,signal,endpoint)=>{calls.push(['verify',kind,path,endpoint]);return{runtime,ok:true};},latest:async()=>({http:null,browser:null}),start:async()=>calls.push('start'),stop:async()=>calls.push('stop'),named:async(project,kind,name)=>{calls.push([kind,name]);return{name,ok:true,exit_code:0};}};
  for(const [key,value] of Object.entries(methods)){const previous=Client.prototype[key];Client.prototype[key]=value;t.after(()=>Client.prototype[key]=previous);}
  const previousBinary=security.executable,previousCLI=local.localJSON;
  security.executable=async()=>'/usr/local/bin/fugato';
  local.localJSON=async(binary,args)=>{calls.push(args);if(args[0]==='projects')return{worktree:root,projects,truncated:false};if(args[0]==='worktrees')return{repository,worktrees:[{path:root,branch:'feature',missing:false}],truncated:false};return{ok:true,checks:[]};};
  t.after(()=>{security.executable=previousBinary;local.localJSON=previousCLI;});
  const context={extensionUri:uri(path.resolve(__dirname,'..')),subscriptions:[],workspaceState:{get:()=>undefined,update:async(key,value)=>{selectionWrites.push(value);}}};
  require('../out/extension').activate(context);
  t.after(()=>context.subscriptions.forEach(subscription=>subscription.dispose()));
  const invoke=(name,row)=>commands.get(`fugato.${name}`)(row);
  await invoke('refresh');
  await invoke('selectWorktree',{target:{repository,worktree:root}});
  const flatten=rows=>rows.flatMap(row=>[row,...flatten(row.children||[])]);
  const roots=()=>views.get('fugato.details').provider.getChildren();
  const details=()=>flatten(roots());
  assert.equal(details().find(row=>row.label==='Branch').description,'feature');
  assert.equal(details().filter(row=>row.command?.command==='fugato.selectProject').length,0,'Discovery stays inside Create environment');
  assert.deepEqual(calls,[],'selecting worktree must only read discovery');
  await invoke('selectProject',{target:{repository,worktree:root,projectDir:projects[0].project_dir}});
  assert.ok(!details().some(row=>row.label==='Open preview'),'Preview remains available through ports and palette, without a duplicate toolbar action');
  assert.ok(!roots().some(row=>row.label==='Create environment'),'Creation is an empty-state action, not an alternative to Start or Stop');
  assert.ok(!details().some(row=>row.label==='Add environment…'));
  assert.ok(!details().some(row=>row.label==='View logs'),'Logs has one navigation control: its tab and service selector');
  assert.ok(!details().some(row=>row.label==='View ports'),'The service inventory is the port view');
  assert.equal(roots()[0].label,'web');assert.equal(roots()[0].description,runtime.id,'Environment ID is a native dimmed description');
  assert.equal(roots().find(row=>row.label==='Configuration & setup').collapsibleState,vscode.TreeItemCollapsibleState.Collapsed);
  const serviceGroup=roots().find(row=>row.label==='Services & ports');
  assert.equal(serviceGroup.collapsibleState,vscode.TreeItemCollapsibleState.Expanded);
  assert.equal(serviceGroup.children.find(row=>row.label==='db').children[0].description,'5432/tcp','Internal port is a dimmed description');
  assert.equal(serviceGroup.children.find(row=>row.label==='db').children[0].command,undefined,'Internal ports never open host URLs');
  const detailIDs=details().map(row=>row.id);assert.equal(new Set(detailIDs).size,detailIDs.length,'Every nested row needs a unique editor identity, including services without ports');
  const servicePort=serviceGroup.children.find(row=>row.label==='dev').children[0];
  await invoke('openService',servicePort);assert.equal(external.pop(),publishedPorts[0].url);
  await invoke('copyServiceAddress',servicePort);assert.equal(external.pop(),publishedPorts[0].url);
  const worktreeRow=views.get('fugato.worktrees').provider.getChildren()[0].children[0];
  assert.match(worktreeRow.description,/Current folder · running/);assert.equal(worktreeRow.children,undefined);
  assert.equal(panels.length,1,'Worktree and environment selection reuse one editor tab');
  await invoke('showLogs',details().find(row=>row.service==='dev'));
  assert.deepEqual(calls.pop(),['logs',runtime.id,'dev','native'],'Log service selectors send the host-owned service name and kind');
  await invoke('showLogs');
  assert.deepEqual(calls.pop(),['logs',runtime.id,undefined,undefined]);
  managementReceive({type:'ready'});
  assert.deepEqual(managementMessages.at(-1),{type:'selectTab',tab:'logs'},'The palette opens Logs through the same tab, including before the page is ready');
  await invoke('createEnvironment');
  assert.ok(!details().some(row=>row.label==='Create environment setup'),'Create selects an existing environment without offering another creation flow');
  assert.equal(roots()[0].description,runtime.id);
  assert.deepEqual(calls,[]);
  registered=[];await invoke('refresh');await invoke('selectWorktree',{target:{repository,worktree:root}});await invoke('createEnvironment');
  const cancelCreate=details().find(row=>row.label==='Cancel');
  assert.equal(cancelCreate.command.command,'fugato.selectWorktree','Setup can return through the existing selection action');
  await invoke('selectWorktree',cancelCreate);
  assert.equal(details().find(row=>row.label==='Worktree path').description,root,'Cancelling setup preserves the worktree');
  assert.ok(!details().some(row=>row.label==='Create environment setup'));
  projects[0].configured=false;
  await invoke('createEnvironment');
  assert.equal(details().find(row=>row.label==='Project path').description,projects[1].project_dir,'A sole valid configuration is selected automatically');
  assert.ok(!details().some(row=>row.label==='Configured projects'),'A sole configuration does not require a project chooser');
  projects[0].configured=true;await invoke('refresh');
  assert.ok(details().some(row=>row.label==='Configured projects'));
  assert.equal(details().filter(row=>row.command?.command==='fugato.selectProject').length,2,'An empty worktree chooses among multiple configured projects');
  assert.ok(!details().some(row=>row.label==='Start'),'Create flow does not offer workload start');
  pickedDirectory=path.join(root,'empty-project');await fs.mkdir(pickedDirectory);
  await invoke('chooseDirectory');await invoke('refresh');
  assert.equal(details().find(row=>row.label==='Project path').description,pickedDirectory,'Explicit directories survive refresh without a discoverable manifest');
  await invoke('editConfiguration');
  assert.equal(shown.at(-1).fsPath,path.join(pickedDirectory,'.fugato.yaml'));
  assert.equal(shown.at(-1).scheme,'untitled','Empty directory configuration opens an unsaved template');
  await invoke('selectProject',{target:{repository,worktree:root,projectDir:projects[1].project_dir}});
  assert.ok(details().some(row=>row.label==='Create'));
  await invoke('createRuntime');
  assert.deepEqual(calls,[['create',projects[1].id,false]],'Creating prepares and registers exactly once without starting');
  assert.equal(details().find(row=>row.label==='Runtime').description,'stopped');
  assert.ok(details().some(row=>row.label==='Start'),'Created environment offers an explicit Start action');
  assert.equal(panels.length,1,'Creation remains in the same management tab');
  registered=[];calls.length=0;
  await invoke('refresh');await invoke('createEnvironment');
  await invoke('selectProject',{target:{repository,worktree:root,projectDir:projects[1].project_dir}});
  Client.prototype.create=async(project,signal)=>{await methods.create(project,signal);throw Error('Environment creation response could not be validated. Creation may have completed');};
  await invoke('createRuntime');
  assert.match(errors.pop(),/Creation may have completed/);
  assert.equal(details().find(row=>row.label==='Runtime').description,'stopped');
  assert.ok(details().some(row=>row.label==='Start'),'Confirmed registration leaves setup even when the create response is malformed');
  assert.ok(!details().some(row=>row.label==='Create'),'A registered environment cannot remain stranded on Create');
  assert.deepEqual(calls,[['create',projects[1].id,false]],'Malformed responses never retry creation or start workloads');
  Client.prototype.create=methods.create;registered=[runtime];calls.length=0;
  await invoke('refresh');await invoke('selectWorktree',{target:{repository,worktree:root,runtime,projectDir:runtime.project_dir}});
  Client.prototype.info=async()=>({instance_id:'test',features:{}});await invoke('refresh');
  assert.ok(details().some(row=>row.label==='Published ports only'),'Older daemons give a clear internal-port update hint');
  assert.ok(!details().some(row=>row.label==='Internal'),'Older daemon results must not invent internal ports');
  assert.ok(details().some(row=>row.label==='Daemon update required'));
  assert.ok(!details().some(row=>['Create environment','Add environment…','Destroy environment','Remove worktree'].includes(row.label)),'Unsupported mutation controls are unavailable');
  const confirmationsBefore=confirmations.length;
  for(const command of ['createWorktree','removeWorktree','destroyRuntime','startDaemon']) {
    await invoke(command);
    assert.match(errors.pop(),/Daemon update required/,'Palette and stale actions check capabilities before dialogs or mutation');
  }
  assert.equal(confirmations.length,confirmationsBefore,'Older daemon failures do not ask for destructive confirmation');
  assert.deepEqual(calls,[],'Older daemons receive no unsupported mutations or implicit startup');
  Client.prototype.info=methods.info;await invoke('refresh');
  await invoke('startDaemon');
  Client.prototype.stop=async()=>{throw new APIError('Environment no longer registered',404);};
  await invoke('stopRuntime');
  const rejectedError=errors.pop();assert.match(rejectedError,/Environment no longer registered/);assert.doesNotMatch(rejectedError,/Outcome may be uncertain|Request rejected|not been retried/,'Expected daemon rejection stays concise');
  let timeoutCalls=0;Client.prototype.stop=async()=>{timeoutCalls++;throw Error('Fugato request timed out');};
  await invoke('stopRuntime');assert.match(errors.pop(),/Outcome may be uncertain/);assert.equal(timeoutCalls,1,'Unknown mutation outcomes are never retried');
  Client.prototype.stop=methods.stop;
  let rejectStartup;const childProcess=require('node:child_process'),originalSpawn=childProcess.spawn;
  childProcess.spawn=()=>{calls.push('unexpected daemon startup');throw Error('Unexpected stale daemon startup');};
  Client.prototype.info=()=>{Client.prototype.info=methods.info;return new Promise((_,reject)=>{rejectStartup=reject;});};
  const staleStartup=invoke('startDaemon');await new Promise(setImmediate);
  configurationChange({affectsConfiguration:()=>true});rejectStartup(Error('connection closed'));
  await staleStartup;await invoke('refresh');
  childProcess.spawn=originalSpawn;
  assert.deepEqual(calls,[],'selecting project must not start runtime or daemon');
  const treeView=views.get('fugato.worktrees'),detailView=views.get('fugato.details');
  const refreshCounts=()=>[treeView.provider.emitter.fires,detailView.provider.emitter.fires,selectionWrites.length,treeView.messages.length];
  const originalTimeout=global.setTimeout;let poll;
  global.setTimeout=(callback,delay,...args)=>{if(delay>=5000){poll=callback;return 0;}return originalTimeout(callback,delay,...args);};
  t.after(()=>{global.setTimeout=originalTimeout;});
  vscode.window.state.focused=true;await invoke('refresh');
  const initialCounts=refreshCounts();
  assert.equal(typeof poll,'function','Focused visible worktree view schedules polling');
  poll();await invoke('refresh');await invoke('refresh');
  assert.deepEqual(refreshCounts(),initialCounts,'Unchanged refreshes must not rerender either view, persist selection, or change the banner');
  assert.equal(treeView.message,undefined,'Healthy daemon polls need no banner or refresh timestamp');
  runtime.updated_at='new daemon metadata';await invoke('refresh');
  assert.deepEqual(refreshCounts(),initialCounts,'Nonvisual daemon metadata must not rerender views or rewrite the selected project');
  publishedPorts[0].host_port=3101;await invoke('refresh');
  assert.equal(treeView.provider.emitter.fires,initialCounts[0],'Port-only changes do not redraw the worktree list');
  assert.equal(detailView.provider.emitter.fires,initialCounts[1]+1,'Changed ports update visible service details');
  publishedPorts[0].host_port=3001;await invoke('refresh');
  serviceError='Docker unavailable';await invoke('refresh');
  assert.equal(details().find(row=>row.label==='Services unavailable').description,'Docker unavailable');
  assert.equal(treeView.message,undefined,'A service inspection error does not mark the daemon unavailable');
  serviceError=undefined;await invoke('refresh');
  const beforeStop=refreshCounts();
  runtime.observed='stopped';await invoke('refresh');
  assert.equal(treeView.provider.emitter.fires,beforeStop[0]+1);
  assert.equal(detailView.provider.emitter.fires,beforeStop[1]+1);
  assert.ok(!roots().some(row=>row.label==='Services & ports'),'Stopping hides stale service addresses');
  assert.ok(details().some(row=>row.service==='dev'&&row.serviceKind==='native'),'Stopping retains service choices for logs');
  assert.equal(details().find(row=>row.label==='Runtime').description,'stopped');
  assert.ok(!details().some(row=>row.label==='Open preview'));
  const readyStopped=details().find(row=>row.checkName==='ready'),lintStopped=details().find(row=>row.checkName==='lint');
  assert.equal(readyStopped.command,undefined);assert.equal(readyStopped.description,'Requires a running environment.');
  assert.ok(lintStopped.command,'An independent check remains available while stopped');
  const beforeBlocked=calls.length;
  const errorsBeforeBlocked=errors.length;
  await invoke('runCheck',readyStopped);assert.equal(calls.length,beforeBlocked);
  assert.equal(errors.length,errorsBeforeBlocked,'An expected stopped-check prerequisite stays inline without an error notification');
  await invoke('runCheck');assert.deepEqual(calls.at(-1),['check','lint'],'The palette offers only checks whose prerequisites are available');
  assert.equal(details().find(row=>row.checkName==='lint').description,'Passed');
  const metadata=projects[0].check_details;projects[0].check_details=undefined;await invoke('refresh');
  assert.equal(details().find(row=>row.checkName==='lint').command,undefined,'Older metadata never assumes a stopped check is independent');
  assert.match(details().find(row=>row.checkName==='lint').description,/Update the daemon/);
  projects[0].check_details=metadata;await invoke('refresh');
  runtime.observed='running';await invoke('refresh');
  const treeIDs=()=>treeView.provider.getChildren().flatMap(repo=>[repo.id,...repo.children.flatMap(worktree=>[worktree.id,...(worktree.children||[]).map(project=>project.id)])]);
  const selectedIDs=treeIDs();
  offline=true;await invoke('refresh');
  assert.match(treeView.message,/Unavailable/);
  assert.ok(!details().some(row=>row.label==='Start Daemon'),'Daemon recovery belongs to the connection controls, not the environment toolbar');
  const offlineCounts=refreshCounts();await invoke('refresh');
  assert.deepEqual(refreshCounts(),offlineCounts,'Repeated connection failures keep cached rows and the diagnostic stable');
  offline=false;await invoke('refresh');
  assert.equal(treeView.message,undefined,'Recovery clears the error banner');
  assert.ok(details().some(row=>row.label==='Stop'));
  assert.deepEqual(treeIDs(),selectedIDs,'Refreshes preserve tree identities for selection and expansion');
  let finishCheck;
  Client.prototype.named=async()=>new Promise(resolve=>{finishCheck=resolve;});
  const checking=invoke('runCheck');await new Promise(setImmediate);
  assert.match(treeView.provider.getChildren()[0].children[0].description,/Checking…/);
  assert.ok(!details().some(row=>row.label==='Stop'));
  assert.ok(!details().some(row=>row.label==='Destroy environment'),'Busy environments cannot be destroyed from details');
  await invoke('refresh');
  assert.match(treeView.provider.getChildren()[0].children[0].description,/Checking…/,'Polling retains an in-flight action state');
  const shownBeforeCheck=shown.length;
  finishCheck({name:'ready',ok:true,exit_code:0});await checking;
  assert.equal(shown.length,shownBeforeCheck,'Check results remain inline without opening a JSON report');
  assert.equal(details().find(row=>row.checkName==='ready').description,'Passed');
  assert.match(treeView.provider.getChildren()[0].children[0].description,/ · running · /);
  assert.ok(details().some(row=>row.label==='Stop'),'Action completion restores the available actions even when daemon data is unchanged');
  let finishLateCheck;Client.prototype.named=async()=>new Promise(resolve=>{finishLateCheck=resolve;});
  const lateCheck=invoke('runCheck',details().find(row=>row.checkName==='ready'));await new Promise(setImmediate);
  await invoke('selectWorktree',{target:{repository,worktree:root,runtime,projectDir:runtime.project_dir}});
  finishLateCheck({name:'ready',ok:false,status:503,exit_code:0});await lateCheck;
  assert.equal(details().find(row=>row.checkName==='ready').description,'Run check','A late check result cannot enter a changed selection context');
  Client.prototype.named=methods.named;
  vscode.window.state.focused=false;global.setTimeout=originalTimeout;
  calls.length=0;
  await invoke('checkSetup');
  assert.deepEqual(calls,[['doctor','--worktree',projects[0].project_dir,'--json']]);
  assert.ok(shown.some(document=>document.scheme==='fugato-report'));
  await invoke('editConfiguration');
  assert.equal(shown.at(-1).fsPath,projects[0].config_file);
  await fs.rm(projects[1].config_file);
  await invoke('editConfiguration',{target:{repository,worktree:root,projectDir:projects[1].project_dir}});
  assert.equal(shown.at(-1).scheme,'untitled');assert.match(edits.at(-1),/^version: 1\n/);
  await assert.rejects(fs.access(projects[1].config_file),'Configuration template must stay unsaved');
  pick=1;await invoke('openPreview');assert.deepEqual(external,['http://admin.aaaaaaaaaaaaaaaa.localhost:46656/']);
  await invoke('verifyHTTP');assert.deepEqual(calls.at(-1),['verify','http','/health','admin']);
  const callsBeforeBrowser=calls.length;await invoke('verifyBrowser');assert.equal(calls.length,callsBeforeBrowser);assert.match(errors.pop(),/requires a Compose browser endpoint/);
  await invoke('runCommand');assert.equal(terminals[0].shellPath,'/usr/local/bin/fugato');assert.deepEqual(terminals[0].shellArgs,['run','repl','--worktree',projects[0].project_dir]);
  pick=0;await invoke('runCommand');assert.deepEqual(calls.at(-1),['run','test']);
  const immediateDiscovery=Client.prototype.projects; let releaseDiscovery;
  Client.prototype.projects=async()=>new Promise(resolve=>{releaseDiscovery=resolve;});
  const oldSelection=invoke('selectWorktree',{target:{repository,worktree:root}});
  await new Promise(setImmediate);
  Client.prototype.projects=immediateDiscovery;
  await invoke('selectWorktree',{target:{repository,worktree:root}});
  await invoke('selectProject',{target:{repository,worktree:root,projectDir:projects[1].project_dir}});
  releaseDiscovery({worktree:root,projects:[projects[0]],truncated:false});await oldSelection;
  assert.equal(details().find(row=>row.label==='Project path').description,projects[1].project_dir,'Late discovery must not overwrite newer project selection');
  let releaseServices;
  Client.prototype.services=async()=>new Promise(resolve=>{releaseServices=resolve;});
  const oldServices=invoke('selectProject',{target:{repository,worktree:root,projectDir:projects[0].project_dir}});
  await new Promise(setImmediate);
  await invoke('selectProject',{target:{repository,worktree:root,projectDir:projects[1].project_dir}});
  releaseServices(services);await oldServices;
  assert.equal(details().find(row=>row.label==='Project path').description,projects[1].project_dir);
  assert.ok(!roots().some(row=>row.label==='Services & ports'),'Late port inventory must not leak into another environment');
  Client.prototype.services=methods.services;
  await invoke('selectProject',{target:{repository,worktree:root,projectDir:projects[0].project_dir}});
  projects[0].error='unsupported .fugato.yaml version 2';change();await new Promise(resolve=>setTimeout(resolve,250));
  assert.equal(details().find(row=>row.label==='Configuration error').description,'unsupported .fugato.yaml version 2');
  assert.ok(!details().some(row=>row.label==='Start'));
  runtime.observed='error'; await invoke('refresh');
  assert.ok(details().some(row=>row.label==='Stop'), 'Stop remains available after an error and invalid config');
  projects[0].error=undefined; projects[0].endpoints=[]; runtime.observed='running'; change(); await new Promise(resolve=>setTimeout(resolve,250));
  assert.ok(!details().some(row=>row.label==='Open preview'), 'No browser action for a project with zero browser endpoints');
  offline=true;await invoke('refresh');await invoke('selectWorktree',{target:{repository,worktree:root}});
  assert.ok(!details().some(row=>row.label==='Start Daemon'),'Connection recovery is not duplicated in environment actions');
  assert.deepEqual(calls.at(-1),['projects','--worktree',root,'--json']);
  assert.ok(!calls.includes('start') && !calls.includes('stop'));
  assert.deepEqual(errors,[]);
  assert.ok(!details().some(row=>row.label==='Destroy environment'),'Cached environments cannot be destroyed offline');

  offline=false;runtime.observed='stopped';await invoke('refresh');
  const runtimeTarget={repository,worktree:root,projectDir:runtime.project_dir,runtime};
  await invoke('selectWorktree',{target:runtimeTarget});
  const destroyRow=details().find(row=>row.label==='Destroy environment');
  assert.equal(destroyRow.contextValue,'destructive');
  await invoke('destroyRuntime',destroyRow);
  assert.equal(calls.filter(call=>call[0]==='destroy').length,0,'Cancel never destroys');
  assert.equal(confirmations.at(-1)[1].modal,true);
  assert.ok(confirmations.at(-1)[1].detail.includes(runtime.id));
  assert.match(confirmations.at(-1)[1].detail,/Volumes and native data are retained/);

  confirmation='Destroy environment';
  Client.prototype.get=async()=>({...runtime,project_dir:projects[1].project_dir});
  await invoke('destroyRuntime',destroyRow);
  assert.match(errors.pop(),/identity changed/);
  assert.equal(calls.filter(call=>call[0]==='destroy').length,0);
  Client.prototype.get=methods.get;
  confirmation=async()=>{await invoke('selectProject',{target:{repository,worktree:root,projectDir:projects[1].project_dir}});return 'Destroy environment';};
  await invoke('destroyRuntime',destroyRow);
  assert.equal(calls.filter(call=>call[0]==='destroy').length,0,'A dialog for an old selection cannot destroy it');

  confirmation=undefined;await invoke('selectWorktree',{target:runtimeTarget});
  assert.ok(!details().some(row=>row.label==='Remove worktree'),'The primary worktree has no removal action');
  await invoke('removeWorktree');assert.equal(errors.pop(),'Fugato: Destroy the environment first.');
  managementReceive({type:'ready'});
  const removalState=managementMessages.filter(message=>message.type==='state').at(-1);
  const removalAction=flatten(removalState.model.details).find(row=>row.label==='lint').action;
  Client.prototype.named=async()=>{throw new APIError('check failed',400);};
  holdErrorNotification=true;
  let settle;
  const settled=new Promise(resolve=>{settle=resolve;});
  onManagementMessage=message=>{if(message.type==='actionFinished'&&message.action===removalAction)settle(true);};
  const watchdog=setTimeout(()=>settle(false),500);
  managementReceive({type:'action',revision:removalState.revision,action:removalAction});
  assert.equal(await settled,true,'The action must finish before its notification is dismissed');clearTimeout(watchdog);onManagementMessage=undefined;
  assert.match(errors.pop(),/lint: check failed/);assert.deepEqual(errorActions.at(-1),[],'Expected check failures have no generic setup action');
  assert.equal(details().find(row=>row.checkName==='lint').description,'Failed: check failed');
  assert.equal(typeof dismissError,'function','The notification remains unresolved during command completion');
  assert.ok(managementMessages.some(message=>message.type==='actionFinished'&&message.action===removalAction),'A failed action releases pending controls before its error notification is dismissed');
  holdErrorNotification=false;dismissError(undefined);
  Client.prototype.named=methods.named;
  const linked=path.join(root,'linked');
  const worktrees=[{path:root,branch:'feature',missing:false}];
  Client.prototype.worktrees=async()=>({repository,worktrees:[...worktrees],truncated:false});
  Client.prototype.createWorktree=async(source,branch,target,newBranch)=>{
    calls.push(['createWorktree',source,branch,target,newBranch]);await fs.mkdir(target);
    worktrees.push({path:target,branch,missing:false});return{repository,worktrees:[...worktrees],truncated:false};
  };
  Client.prototype.removeWorktree=async(source,target)=>{calls.push(['removeWorktree',source,target]);worktrees.splice(worktrees.findIndex(worktree=>worktree.path===target),1);};
  const originalInput=vscode.window.showInputBox;let inputs=['next','linked'];
  vscode.window.showInputBox=async()=>inputs.shift();pickedDirectory=root;pick=1;
  await invoke('createWorktree');
  assert.deepEqual(calls.at(-1),['createWorktree',root,'next',linked,true],'Worktree creation uses the chosen branch, directory and mode');
  await invoke('removeWorktree');
  assert.equal(calls.filter(call=>call[0]==='removeWorktree').length,0,'Cancelled worktree removal never mutates Git');
  const removeLinked=Client.prototype.removeWorktree;
  const prefix='cannot remove a worktree with uncommitted, untracked, or ignored files';
  for (const message of [prefix+'\n\nLocal files:\n- Changed: .fugato.yaml\n- Ignored: .env\n\nCommit or stash tracked changes. Move untracked or ignored files outside this worktree.',prefix]) {
    let attempts=0;
    Client.prototype.removeWorktree=async()=>{attempts++;throw new APIError(message,400);};
    confirmation=title=>title==='Worktree was not removed'?'Open folder':'Remove worktree';
    const beforeErrors=errors.length;
    await invoke('removeWorktree');
    assert.equal(attempts,1,'Blocked removal is never retried');
    assert.equal(errors.length,beforeErrors,'Expected refusal has guidance instead of a generic error toast');
    assert.equal(confirmations.at(-1)[0],'Worktree was not removed');
    assert.match(confirmations.at(-1)[1].detail,/Commit or stash/);
    assert.match(confirmations.at(-1)[1].detail,/outside this worktree/);
    if(message.includes('Local files:'))assert.match(confirmations.at(-1)[1].detail,/Ignored: .env/);
    assert.equal(openedFolders.at(-1)[0].fsPath,linked);
    assert.deepEqual(openedFolders.at(-1)[1],{forceNewWindow:true});
  }
  Client.prototype.removeWorktree=async()=>{throw new APIError('daemon failure',500);};
  confirmation='Remove worktree';await invoke('removeWorktree');
  assert.match(errors.pop(),/daemon failure/,'Unexpected failures retain the normal error handling');
  Client.prototype.removeWorktree=removeLinked;
  confirmation='Remove worktree';await invoke('removeWorktree');
  assert.deepEqual(calls.at(-1),['removeWorktree',root,linked],'Only the confirmed linked worktree is removed');
  await invoke('selectWorktree',{target:runtimeTarget});
  inputs=['next','stale'];vscode.window.showInputBox=async()=>{const value=inputs.shift();if(value==='stale')await invoke('selectWorktree',{target:runtimeTarget});return value;};
  const createCount=calls.filter(call=>call[0]==='createWorktree').length;
  await invoke('createWorktree');
  assert.equal(calls.filter(call=>call[0]==='createWorktree').length,createCount,'Selection changes cancel stale worktree creation dialogs');
  vscode.window.showInputBox=originalInput;pickedDirectory=undefined;confirmation=undefined;

  const worker={...runtime,id:projects[1].id,project_dir:projects[1].project_dir};registered=[runtime,worker];
  Client.prototype.worktrees=async()=>({repository,worktrees:[],truncated:false});
  Client.prototype.projects=async()=>{throw Error('worktree folder missing');};
  Client.prototype.get=async id=>{const record=registered.find(record=>record.id===id);assert.ok(record,'Destroy must not read the deleted runtime afterward');return record;};
  await invoke('refresh');
  const missingRow=treeView.provider.getChildren()[0].children.find(row=>row.label==='Unavailable worktrees').children[0];
  assert.equal(missingRow.destroyCommand.command,'fugato.destroyRuntime','Missing worktrees expose an inline trash action');
  assert.equal(missingRow.children.length,2);
  confirmation=undefined;pick=1;await invoke('destroyRuntime',missingRow);
  assert.ok(confirmations.at(-1)[1].detail.includes(worker.id),'Multiple environments require an explicit choice');
  assert.equal(calls.filter(call=>call[0]==='destroy').length,0);
  Client.prototype.projects=async()=>({worktree:root,projects:[projects[1]],truncated:false});
  await invoke('selectWorktree',missingRow.children[0]);
  assert.ok(details().some(row=>row.label==='Choose environment'),'Preexisting multiple environments retain their selector');
  assert.equal(roots()[0].description,runtime.id,'A removed configuration cannot redirect selection to the only remaining project');
  assert.ok(details().some(row=>row.label==='Destroy environment'),'Registered runtime remains removable without a discovered configuration');
  Client.prototype.projects=async()=>{throw Error('worktree folder missing');};
  await invoke('selectWorktree',missingRow.children[0]);
  assert.ok(details().some(row=>row.label==='Destroy environment'),'Missing folders and failed discovery cannot strand cleanup');
  confirmation='Destroy environment';await invoke('destroyRuntime',missingRow.children[0]);
  assert.deepEqual(calls.filter(call=>call[0]==='destroy'),[['destroy',runtime.id,false]],'Destroy uses the exact confirmed ID and a live cancellation signal, once');
  assert.deepEqual(registered,[worker],'Other environments remain registered');
  assert.ok(!flatten(treeView.provider.getChildren()).some(row=>row.id===`runtime:${runtime.id}`),'Destroyed runtime disappears after refresh');
  assert.equal(roots()[0].description,worker.id,'Destroy preserves the worktree context and selects its remaining environment');
  assert.match(reports.get('fugato-report').provideTextDocumentContent(uri(`/${runtime.id}.json`,'fugato-report')),/Environment destroyed/);
  assert.deepEqual(errors,[]);
});
