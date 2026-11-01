const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
const http = require('node:http');
const {Client} = require('../out/client');
const {endpointURL, repository, runtime, info, latest, verifyPath} = require('../out/contracts');
const {readToken, settings, screenshot, executable, plain} = require('../out/security');
const {groups, revalidate} = require('../out/model');
const id = '0123456789abcdef';
const record = {id, repository:'/repo/.git', worktree:'/repo', project_dir:'/repo/app', commit:'abc', desired:'running', observed:'running', preview_url:`http://web.${id}.localhost:46656/`};
async function temporary(t) { const dir = await fs.mkdtemp(path.join(os.tmpdir(), 'fugato-extension-')); t.after(() => fs.rm(dir, {recursive:true,force:true})); return dir; }
async function daemon(t, handler) {
  const directory = await temporary(t); await fs.writeFile(path.join(directory,'token'),'a'.repeat(64),{mode:0o600});
  const server = http.createServer(handler); await new Promise(resolve => server.listen(0,'127.0.0.1',resolve));
  t.after(() => new Promise(resolve => { server.closeAllConnections(); server.close(resolve); }));
  const client = new Client({stateDirectory: directory, apiPort: server.address().port, executablePath:''}); t.after(() => client.dispose());
  return {client, directory};
}
test('previews require named endpoint identity and an explicit port', () => {
  const binding={name:'web',service:'frontend',container_port:3000,host_port:3001,protocol:'tcp',host_ip:'127.0.0.1',browser:true,url:record.preview_url};
  assert.equal(endpointURL(binding,id),record.preview_url);
  assert.equal(endpointURL({...binding,url:`http://web.${id}.localhost:80/`},id),`http://web.${id}.localhost:80/`);
  for(const url of [`http://${id}.localhost:46656/`,`http://web.${id}.localhost/`,`http://web.${id}.localhost:46656/path`,`http://web.${id}.localhost:46656/a/..`,`http://web.${id}.localhost:46656/?q=a`,`http://user@web.${id}.localhost:46656/`,`http://web.${id}.localhost.evil:46656/`]){assert.throws(()=>endpointURL({...binding,url},id));}
  assert.throws(()=>endpointURL({...binding,name:undefined},id));
});
test('DTOs reject malformed identity and missing negotiated features', () => {
  assert.equal(runtime(record).id,id);
  assert.throws(() => runtime({...record,id:'../escape'}));
  assert.throws(() => runtime({...record, project_dir:'relative'}));
  assert.throws(() => info({api_version:'v1',instance_id:'x',features:{}}));
  const features={repository_worktrees:true,project_resolution:true,latest_verification:true};
  assert.throws(()=>info({api_version:'v1',instance_id:'x',features:{...features,environment_v2:true}}));
  assert.equal(info({api_version:'v1',instance_id:'x',features:{...features,environments:true}}).features.environments,true);
  assert.throws(() => repository({repository:'/r', worktrees:[{path:'/r'}],truncated:false}));
  assert.deepEqual(latest({http:null,browser:null},id),{http:null,browser:null});
});
test('configuration precedence and absolute paths', () => {
  assert.deepEqual(settings({stateDirectory:'/explicit',apiPort:49999},{FUGATO_STATE_DIR:'/env',FUGATO_API_PORT:'49998'}),{stateDirectory:'/explicit',apiPort:49999,executablePath:''});
  assert.equal(settings({}, {FUGATO_STATE_DIR:'/env',FUGATO_API_PORT:'49998'}).apiPort,49998);
  assert.throws(() => settings({stateDirectory:'relative'},{})); assert.throws(() => settings({}, {FUGATO_API_PORT:'NaN'}));
});
test('token checks permissions, symlinks, size, and hex format', async t => {
  const dir = await temporary(t), file = path.join(dir,'token');
  await fs.writeFile(file,'a'.repeat(64),{mode:0o600}); assert.equal(await readToken(dir),'a'.repeat(64));
  await fs.chmod(file,0o644); await assert.rejects(readToken(dir),/user-only/);
  await fs.chmod(file,0o600); await fs.writeFile(file,'a'.repeat(100)); await assert.rejects(readToken(dir));
  await fs.writeFile(file,'z'.repeat(64)); await assert.rejects(readToken(dir),/format/);
  await fs.rename(file,path.join(dir,'real')); await fs.symlink(path.join(dir,'real'),file); await assert.rejects(readToken(dir));
});
test('screenshots validate runtime directory, magic, size and symlink escape', async t => {
  const dir = await temporary(t), artifacts = path.join(dir,'artifacts',id); await fs.mkdir(artifacts,{recursive:true});
  const file = path.join(artifacts,'shot.png'); const png = Buffer.from([137,80,78,71,13,10,26,10]);
  await fs.writeFile(file,png); assert.equal(await screenshot(dir,id,file),await fs.realpath(file));
  const outside = path.join(dir,'outside.png'); await fs.writeFile(outside,png);
  await assert.rejects(screenshot(dir,id,outside));
  const escaped = path.join(artifacts,'escape.png'); await fs.symlink(outside,escaped); await assert.rejects(screenshot(dir,id,escaped));
  await fs.writeFile(file,'not png'); await assert.rejects(screenshot(dir,id,file),/not a PNG/);
  await fs.truncate(file,8*1024*1024+1); await assert.rejects(screenshot(dir,id,file),/8 MiB/);
});
test('executable resolver excludes workspace and relative PATH entries', async t => {
  const dir = await temporary(t), workspace = path.join(dir,'workspace'), trusted = path.join(dir,'bin');
  await fs.mkdir(workspace); await fs.mkdir(trusted); await fs.writeFile(path.join(workspace,'fugato'),'',{mode:0o755}); await fs.writeFile(path.join(trusted,'fugato'),'',{mode:0o755});
  assert.equal(await executable('',[workspace],`.${path.delimiter}${workspace}${path.delimiter}${trusted}`),await fs.realpath(path.join(trusted,'fugato')));
  await assert.rejects(executable(path.join(workspace,'fugato'),[workspace]));
});
test('repository joins never cross-target; missing worktrees remain visible', () => {
  const repositories = [{repository:'/repo/.git',worktrees:[{path:'/repo',missing:false},{path:'/sibling',missing:false}],truncated:false}];
  const data = groups([...repositories,...repositories],[record,{...record,id:'1111111111111111',worktree:'/removed'},{...record,id:'2222222222222222',repository:'/other/.git'}]);
  assert.equal(data.length,1); assert.equal(data[0].runtimes.length,2); assert.equal(data[0].repository.worktrees.length,3);
  assert.equal(data[0].repository.worktrees.at(-1).missing,true);
  assert.equal(revalidate({repository:'/other/.git',worktree:'/repo',runtime:record},data),undefined);
  assert.equal(revalidate({repository:'/repo/.git',worktree:'/repo',runtime:record},data).runtime.id,id);
  assert.equal(revalidate({repository:'/repo/.git',worktree:'/repo',runtime:{...record,id:'ffffffffffffffff'}},data),undefined,'A stale action target cannot fall back to a different registered environment');
});
test('transport authenticates exact loopback and retries reads once after token rotation', async t => {
  let calls = 0; let directory;
  const fixture = await daemon(t,async (req,res) => { calls++; if(calls===1){ await fs.writeFile(path.join(directory,'token'),'b'.repeat(64)); res.writeHead(401); res.end('{"error":"unauthorized"}'); } else { assert.equal(req.headers.authorization,`Bearer ${'b'.repeat(64)}`); res.end(JSON.stringify(record)); } }); directory=fixture.directory;
  assert.equal((await fixture.client.get(id)).id,id); assert.equal(calls,2);
});
test('mutations never retry after authorization failure', async t => {
  let calls = 0; const {client} = await daemon(t,(req,res) => { calls++; res.writeHead(401); res.end('{"error":"unauthorized"}'); });
  await assert.rejects(client.stop(id),/unauthorized/); assert.equal(calls,1);
});
test('transport rejects redirects, malformed JSON, and oversized responses', async t => {
  let mode = 'redirect'; const {client} = await daemon(t,(req,res) => {
    if(mode==='redirect'){res.writeHead(302,{Location:'https://example.com'});res.end('{}');}
    if(mode==='malformed'){res.end('{');}
    if(mode==='large'){res.end('x'.repeat(2*1024*1024+1));}
  });
  await assert.rejects(client.get(id),/302/); mode='malformed'; await assert.rejects(client.get(id),/Malformed JSON/); mode='large'; await assert.rejects(client.get(id),/2 MiB/);
});
test('cancellation and disposal abort pending requests without retry', async t => {
  let calls=0; const {client}=await daemon(t,()=>{calls++;}); const controller = new AbortController();
  const request=client.stop(id,controller.signal); setTimeout(()=>controller.abort(),20); await assert.rejects(request); assert.equal(calls,1);
  const next=client.get(id); setTimeout(()=>client.dispose(),20); await assert.rejects(next,/closed/); await assert.rejects(client.get(id),/cancelled/);
});
test('first start sends canonical directory and expected runtime identity', async t => {
  const {client}=await daemon(t,(req,res)=>{let text='';req.on('data',chunk=>text+=chunk);req.on('end',()=>{assert.equal(req.url,'/v1/runtimes');assert.deepEqual(JSON.parse(text),{path:record.project_dir,expected_runtime_id:id});res.end('{}');});});
  await client.start({...record,registered:false});
});
test('create, worktree lifecycle and filtered logs use explicit non-retried contracts', async t => {
  const requests=[];
  const {client}=await daemon(t,(req,res)=>{let text='';req.on('data',chunk=>text+=chunk);req.on('end',()=>{
    requests.push([req.url,JSON.parse(text)]);
    if(req.url==='/v1/runtimes/create')res.end(JSON.stringify({...record,desired:'stopped',observed:'stopped'}));
    else if(req.url.endsWith('/create'))res.end(JSON.stringify({repository:record.repository,worktrees:[],truncated:false}));
    else if(req.url.endsWith('/remove'))res.end(JSON.stringify({removed:true}));
    else res.end(JSON.stringify({runtime:record,text:'worker output',truncated:false}));
  });});
  assert.equal((await client.create({...record,registered:false})).observed,'stopped');
  await client.createWorktree('/repo','feature','/sibling',true);
  await client.removeWorktree('/repo','/sibling');
  assert.equal((await client.logs(id,'worker','native')).text,'worker output');
  assert.deepEqual(requests,[['/v1/runtimes/create',{path:record.project_dir,expected_runtime_id:id}],['/v1/repositories/worktrees/create',{path:'/repo',branch:'feature',target_path:'/sibling',new_branch:true}],['/v1/repositories/worktrees/remove',{path:'/repo',worktree:'/sibling'}],[`/v1/agent/runtimes/${id}/logs`,{lines:200,service:'worker',service_kind:'native'}]]);
});
test('create rejects legacy raw responses without repeating a committed mutation', async t => {
  let calls=0;
  const {commit,...legacy}=record;
  const {client}=await daemon(t,(req,res)=>{calls++;res.end(JSON.stringify({...legacy,desired:'stopped',observed:'stopped'}));});
  await assert.rejects(client.create({...record,registered:false}),/Creation may have completed/);
  assert.equal(calls,1,'A legacy response cannot trigger a retry');
});
test('clean relative verification paths and plain output',()=>{
  assert.equal(verifyPath('/checkout?item=1'),'/checkout?item=1');
  for(const p of ['//evil','/a/../b','http://evil','/a#hash','/a\\b']){assert.throws(()=>verifyPath(p));}
  assert.equal(plain('\u001b[31mred\u001b[0m\0'),'red');
});

test('project discovery rejects escaping paths and preserves invalid and native projects', () => {
  const {discovery} = require('../out/contracts');
  const project = {project_dir:'/repo/app',config_file:'/repo/app/.fugato.yaml',compose_files:[],kind:'native',configured:true,commands:[{name:'dev',mode:'service'}],checks:['ready'],endpoints:[{name:'web',command:'dev',port:0,protocol:'http',browser:true}]};
  assert.equal(discovery({worktree:'/repo',projects:[project],truncated:false},'/repo').projects[0].endpoints[0].browser,true);
  const check_details=[{name:'ready',requires_running:true,reason:'Requires a running endpoint'}];
  assert.deepEqual(discovery({worktree:'/repo',projects:[{...project,check_details}],truncated:false},'/repo').projects[0].check_details,check_details);
  assert.equal(discovery({worktree:'/repo',projects:[{...project,error:'bad configuration'}],truncated:true},'/repo').truncated,true);
  for (const patch of [{project_dir:'/outside'},{config_file:'/repo/app/secret'},{compose_files:['/outside/compose.yaml']},{commands:[{name:'dev',mode:'unknown'}]},{check_details:[{name:'ready',requires_running:'yes'}]},{check_details:[{name:'unconfigured',requires_running:false}]}]) {
    assert.throws(()=>discovery({worktree:'/repo',projects:[{...project,...patch}],truncated:false},'/repo'));
  }
  assert.throws(()=>discovery({worktree:'/other',projects:[],truncated:false},'/repo'));
});
test('browser endpoints require declared intent and exact loopback binding URL', () => {
  const {endpointURL} = require('../out/contracts');
  const binding = {name:'web',service:'',command:'dev',container_port:0,host_port:3001,protocol:'tcp',host_ip:'127.0.0.1',browser:true,url:`http://web.${id}.localhost:46656/`};
  assert.equal(endpointURL(binding,id),binding.url);
  assert.equal(endpointURL({...binding,url:'https://127.0.0.1:3001/'},id),'https://127.0.0.1:3001/');
  for(const patch of [{browser:false},{host_ip:'0.0.0.0'},{name:'other'},{url:'https://evil.example/'},{url:'http://user@127.0.0.1:3001/'},{url:'http://127.0.0.1:3001/path'}]){assert.throws(()=>endpointURL({...binding,...patch},id));}
});
test('read-only discovery, bindings and project tasks use explicit API routes', async t => {
  const calls=[]; const project={project_dir:'/repo/app',configured:true,compose_files:[],kind:'native',commands:[],checks:[],endpoints:[]};
  const {client}=await daemon(t,(req,res)=>{
    let body='';req.on('data',chunk=>body+=chunk);req.on('end',()=>{
      calls.push({method:req.method,route:req.url,body:body ? JSON.parse(body):undefined});
      if(req.url.startsWith('/v1/projects?')){res.end(JSON.stringify({worktree:'/repo',projects:[project],truncated:false}));}
      else if(req.url.endsWith('/ports')||req.url.endsWith('/services')){res.end('[]');} else {res.end('{"name":"test","exit_code":7,"stdout":"","stderr":"failure"}');}
    });
  });
  assert.equal((await client.projects('/repo')).projects.length,1);
  assert.deepEqual(await client.ports(id),[]);
  assert.deepEqual(await client.services(id),[]);
  assert.equal((await client.named(project,'run','test')).exit_code,7);
  assert.deepEqual(calls.map(call=>[call.method,call.route]),[['GET','/v1/projects?path=%2Frepo'],['GET',`/v1/runtimes/${id}/ports`],['GET',`/v1/runtimes/${id}/services`],['POST','/v1/projects/run']]);
  assert.deepEqual(calls[3].body,{path:'/repo/app',name:'test'});
});
test('service inventory preserves internal ports and rejects unsafe host actions', () => {
  const {services,serviceAddress} = require('../out/contracts');
  const internal={service:'db',container_port:5432,host_ip:'',host_port:0,protocol:'tcp'};
  assert.equal(services([{name:'db',kind:'compose',state:'running',ports:[internal]}])[0].ports[0].container_port,5432);
  assert.throws(()=>serviceAddress(internal,id));
  assert.throws(()=>services([{name:'db',kind:'unknown',state:'running',ports:[]}]));
  assert.throws(()=>services([{name:'db',kind:'compose',state:'running',ports:[{...internal,container_port:65536}]}]));
  assert.throws(()=>serviceAddress({...internal,host_ip:'0.0.0.0',host_port:5432},id));
  assert.equal(serviceAddress({...internal,host_ip:'127.0.0.1',host_port:32001},id),'127.0.0.1:32001');
  assert.equal(serviceAddress({...internal,host_ip:'127.0.0.1',host_port:32001,protocol:'http',url:'https://evil.example/'},id),'http://127.0.0.1:32001/');
});
test('verification passes the explicitly selected endpoint through the existing API', async t => {
  const {client}=await daemon(t,(req,res)=>{let body='';req.on('data',chunk=>body+=chunk);req.on('end',()=>{
    assert.equal(req.url,`/v1/agent/runtimes/${id}/verify/http`);
    assert.deepEqual(JSON.parse(body),{path:'/health',timeout_ms:60000,endpoint:'api'});
    res.end(JSON.stringify({runtime:record,url:'http://127.0.0.1:3001/health',ok:true,duration_ms:1,issues:[]}));
  });});
  assert.equal((await client.verify(id,'http','/health',undefined,'api')).ok,true);
});
test('destroy uses the explicit runtime ID, retains data and validates the result',async t=>{
  let result={id,volumes:false},count=0;
  const {client}=await daemon(t,(req,res)=>{let body='';req.on('data',chunk=>body+=chunk);req.on('end',()=>{
    count++;assert.equal(req.method,'POST');assert.equal(req.url,`/v1/runtimes/${id}/destroy`);assert.deepEqual(JSON.parse(body),{volumes:false});res.end(JSON.stringify(result));
  });});
  await client.destroy(id);assert.equal(count,1);
  result={id:'aaaaaaaaaaaaaaaa',volumes:false};await assert.rejects(client.destroy(id),/mismatch/);
  result={id,volumes:true};await assert.rejects(client.destroy(id),/mismatch/);
  const controller=new AbortController();controller.abort();await assert.rejects(client.destroy(id,controller.signal));assert.equal(count,3,'Canceled calls do not reach the daemon');
  await assert.rejects(client.destroy('../outside'));assert.equal(count,3);
});
test('destroy never retries a rejected mutation',async t=>{
  let calls=0,status=401,message='denied';const {client}=await daemon(t,(req,res)=>{calls++;res.writeHead(status);res.end(JSON.stringify({error:message}));});
  await assert.rejects(client.destroy(id));assert.equal(calls,1);
  status=404;await assert.rejects(client.destroy(id),/Daemon update required: environment destruction endpoint unavailable/);assert.equal(calls,2);
  status=400;message='unknown runtime';await assert.rejects(client.destroy(id),/Environment no longer registered/);assert.equal(calls,3);
});
