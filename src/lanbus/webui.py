"""Zero-dependency web dashboard for the lanbus hub."""

HTML = r"""<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>lanbus · local agent bus</title>
<style>
:root{color-scheme:dark;--bg:#0d1117;--panel:#161b22;--line:#30363d;--text:#e6edf3;--muted:#8b949e;--accent:#2f81f7;--ok:#3fb950;--warn:#d29922;--bad:#f85149}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font:14px/1.55 ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif}
header{display:flex;gap:12px;align-items:center;padding:12px 18px;border-bottom:1px solid var(--line);background:#010409;position:sticky;top:0;z-index:5}
h1{font-size:17px;margin:0;letter-spacing:.3px}.sp{flex:1}
input,textarea,button{font:inherit;color:var(--text);background:#0d1117;border:1px solid var(--line);border-radius:8px;padding:8px 10px}
button{cursor:pointer;background:#21262d}button:hover{border-color:var(--accent)}button.primary{background:var(--accent);border-color:var(--accent);color:#fff}
textarea{width:100%;min-height:96px;resize:vertical}input:focus,textarea:focus{outline:none;border-color:var(--accent)}
.layout{display:grid;grid-template-columns:320px minmax(0,1fr);min-height:calc(100vh - 57px)}
aside{border-right:1px solid var(--line);padding:14px;overflow:auto;max-height:calc(100vh - 57px);position:sticky;top:57px}
main{padding:20px 26px;max-width:1100px}@media(max-width:900px){.layout{display:block}aside{position:static;max-height:none;border-right:0;border-bottom:1px solid var(--line)}main{padding:16px}}
.k{color:var(--muted);font-size:12px;text-transform:uppercase;letter-spacing:.8px}.muted{color:var(--muted)}.row{display:flex;gap:8px;align-items:center;flex-wrap:wrap}
.card{background:var(--panel);border:1px solid var(--line);border-radius:12px;padding:14px;margin:12px 0}
.conv{padding:10px;border:1px solid transparent;border-radius:9px;cursor:pointer;margin:6px 0;background:#0d1117}
.conv:hover{border-color:var(--line)}.conv.active{border-color:var(--accent);background:#1f6feb22}
.id{font-family:ui-monospace,SFMono-Regular,Consolas,monospace;font-size:12px;color:var(--accent)}
.pill{display:inline-block;padding:1px 8px;border-radius:999px;background:#21262d;color:var(--muted);font-size:12px;margin:2px 4px 2px 0}
.pill.on,.pill.completed{color:var(--ok)}.pill.partial{color:var(--warn)}
.turn{border-left:3px solid var(--line);padding:8px 0 8px 14px;margin:12px 0}.turn .who{font-weight:600}.turn .meta{font-size:12px;color:var(--muted);margin-left:8px}
.turn pre{white-space:pre-wrap;word-break:break-word;margin:8px 0 0;font:inherit;color:#c9d1d9}
.busy{opacity:.55;pointer-events:none}.err{color:var(--bad);white-space:pre-wrap}
.toast{position:fixed;right:18px;bottom:18px;background:#21262d;border:1px solid var(--line);padding:10px 14px;border-radius:10px;display:none;max-width:420px;z-index:9}.toast.show{display:block}.small{font-size:12px}.mono{font-family:ui-monospace,SFMono-Regular,Consolas,monospace}
</style>
</head>
<body>
<header>
  <h1>lanbus</h1><span class="muted small" id="health">connecting…</span><span class="sp"></span>
  <input id="token" type="password" placeholder="agent token" style="width:210px">
  <button onclick="saveToken()">连接</button><button onclick="refreshAll()">刷新</button>
</header>
<div class="layout">
<aside>
  <div class="row" style="justify-content:space-between"><span class="k">在线环境</span><button class="small" onclick="loadEnvs()">刷新</button></div>
  <div id="envs" class="small muted">加载中…</div>
  <div class="row" style="justify-content:space-between;margin-top:18px"><span class="k">远程目标确认</span><button class="small" onclick="loadApprovals()">刷新</button></div>
  <div id="approvals" class="small muted">加载中…</div>
  <div class="row" style="justify-content:space-between;margin-top:18px"><span class="k">对话历史</span><button class="small" onclick="loadConversations()">刷新</button></div>
  <div id="convs" class="small muted">加载中…</div>
</aside>
<main>
  <div id="banner" class="card" style="display:none"></div>
  <section id="view">
    <div class="card">
      <div class="k">新建本机多 agent 对话</div>
      <div class="row" style="margin-top:10px">
        <input id="envsInput" style="flex:1;min-width:260px" placeholder="env1,env2,…（留空使用所有在线 env）">
        <input id="rounds" type="number" min="1" max="6" value="2" style="width:76px" title="轮数">
        <input id="timeout" type="number" min="30" max="900" value="240" style="width:96px" title="单轮超时秒">
      </div>
      <textarea id="message" placeholder="对话主题 / 开场消息…" style="margin-top:10px"></textarea>
      <div class="row" style="margin-top:10px"><button class="primary" onclick="startChat()">开始对话</button><span id="busy" class="muted small"></span></div>
    </div>
    <div id="current"></div>
  </section>
</main>
</div>
<div id="toast" class="toast"></div>
<script>
const $=s=>document.querySelector(s);let currentId=null;
function token(){return localStorage.getItem('lanbus_token')||$('#token').value||''}
function saveToken(){localStorage.setItem('lanbus_token',$('#token').value.trim());toast('token 已保存');refreshAll()}
function toast(msg){const t=$('#toast');t.textContent=msg;t.classList.add('show');clearTimeout(t._t);t._t=setTimeout(()=>t.classList.remove('show'),3800)}
async function api(path,opt={}){const r=await fetch(path,{...opt,headers:{...(opt.headers||{}),'Authorization':'Bearer '+token(),'Content-Type':'application/json'}});if(!r.ok)throw new Error(r.status+' '+(await r.text()).slice(0,500));return await r.json()}
function esc(s){return String(s??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]))}
async function loadHealth(){try{const j=await (await fetch('/health')).json();$('#health').textContent='hub '+j.version+' · queued '+j.counts.queued+' · running '+j.counts.running}catch(e){$('#health').textContent='hub 离线';$('#health').className='small err'}}
async function loadEnvs(){try{const j=await api('/api/envs');const box=$('#envs');box.innerHTML='';j.workers.filter(w=>w.online).forEach(w=>{const s=document.createElement('span');s.className='pill on';s.textContent=(w.env||w.name)+' · '+(w.agent||'?');s.title=(w.caps||[]).join(',');box.appendChild(s)});if(!j.workers.some(w=>w.online))box.innerHTML='<span class="muted">没有在线 worker</span>'}catch(e){$('#envs').innerHTML='<span class="err">'+esc(e.message)+'</span>'}}
async function loadApprovals(){try{const j=await api('/api/confirmations');const box=$('#approvals');box.innerHTML='';(j.targets||[]).forEach(t=>{const w=(t.workers||[])[0]||{};const d=document.createElement('div');d.className='card';d.style.padding='8px';d.innerHTML='<div><span class="id">'+esc(t.env)+'</span> '+ (t.approved?'<span class="pill completed">已确认</span>':'<span class="pill partial">待确认</span>')+'</div><div class="muted small">'+esc(w.hostname||'')+' · '+esc(w.agent||'')+' · '+esc(w.cwd||'(cwd unknown)')+'</div>'+(t.approved?'<div class="muted small">expires '+esc(t.approval_expires_at||'')+'</div>':'');const b=document.createElement('button');b.textContent=t.approved?'撤销':'确认此 agent';b.style.marginTop='6px';b.onclick=()=>t.approved?revokeTarget(t.env):approveTarget(t.env);d.appendChild(b);box.appendChild(d)});if(!(j.targets||[]).length)box.innerHTML='<span class="muted">没有待确认的远程目标</span>'}catch(e){$('#approvals').innerHTML='<span class="err">'+esc(e.message)+'</span>'}}
async function approveTarget(env){try{await api('/confirm/approve',{method:'POST',body:JSON.stringify({env,ttl_s:1800,approved_by:'web-operator'})});toast('已确认 '+env+'（30 分钟）');loadApprovals();loadEnvs()}catch(e){toast(e.message)}}
async function revokeTarget(env){try{await api('/confirm/revoke',{method:'POST',body:JSON.stringify({env})});toast('已撤销 '+env);loadApprovals();loadEnvs()}catch(e){toast(e.message)}}
async function loadConversations(){try{const j=await api('/conversations?limit=50');const box=$('#convs');box.innerHTML='';(j.conversations||[]).forEach(c=>{const d=document.createElement('div');d.className='conv'+(c.conversation_id===currentId?' active':'');d.onclick=()=>openConv(c.conversation_id);d.innerHTML='<div><span class="id">'+esc(c.conversation_id)+'</span> <span class="pill '+(c.status||'')+'">'+esc(c.status||'')+'</span></div><div class="muted small">'+esc((c.envs||[]).join(' · '))+' · '+c.rounds+' 轮 · '+c.turn_count+' turns</div>';box.appendChild(d)});if(!(j.conversations||[]).length)box.innerHTML='<span class="muted">暂无对话</span>'}catch(e){$('#convs').innerHTML='<span class="err">'+esc(e.message)+'</span>'}}
async function openConv(id){try{const c=await api('/conversations/'+encodeURIComponent(id));currentId=id;renderConversation(c);loadConversations()}catch(e){toast(e.message)}}
function renderConversation(c){const box=$('#current');box.innerHTML='';const head=document.createElement('div');head.className='card';head.innerHTML='<div class="row" style="justify-content:space-between"><div><span class="k">会话</span> <span class="mono">'+esc(c.conversation_id)+'</span></div><span class="pill '+(c.status||'')+'">'+esc(c.status||'')+'</span></div><div class="muted small" style="margin-top:6px">'+esc((c.envs||[]).join(' · '))+' · '+c.rounds+' 轮 · '+c.turn_count+' turns</div><div style="margin-top:10px">'+esc(c.seed||'')+'</div>';box.appendChild(head);(c.turns||[]).forEach(t=>{const d=document.createElement('div');d.className='turn';d.innerHTML='<div><span class="who">'+esc(t.env)+'</span><span class="meta">R'+esc(t.round)+' · '+esc(t.status)+' · '+esc(t.task_id||'')+'</span></div><pre>'+esc(t.reply||'')+'</pre>';box.appendChild(d)});const f=document.createElement('div');f.className='card';f.innerHTML='<div class="k">继续这个会话</div><textarea id="nextMsg" placeholder="继续说什么…"></textarea><div class="row" style="margin-top:10px"><input id="nextRounds" type="number" min="1" max="6" value="1" style="width:76px"><button class="primary" onclick="continueChat()">发送</button><span id="busy2" class="muted small"></span></div>';box.appendChild(f)}
async function startChat(){const msg=$('#message').value.trim();if(!msg){toast('请先写对话主题');return}let envs=($('#envsInput').value||'').split(',').map(x=>x.trim()).filter(Boolean);if(!envs.length){try{const j=await api('/api/envs');envs=[...new Set((j.workers||[]).filter(w=>w.online).map(w=>w.env))]}catch(e){toast(e.message);return}}if(envs.length<2){toast('至少需要两个在线 env');return}await runChat({envs,message:msg,rounds:+$('#rounds').value||2,timeout_s:+$('#timeout').value||240,title:'Web 对话'})}
async function continueChat(){if(!currentId)return;const msg=$('#nextMsg').value.trim();if(!msg){toast('请写继续内容');return}await runChat({conversation_id:currentId,message:msg,rounds:+$('#nextRounds').value||1})}
async function runChat(args){const busy=document.getElementById(currentId?'busy2':'busy');if(busy)busy.textContent='执行中…（每个 agent 依次回复）';$('#view').classList.add('busy');try{const out=await api('/chat',{method:'POST',body:JSON.stringify(args)});if(out.error)throw new Error(out.error);currentId=out.conversation_id;renderConversation(out);await loadConversations();toast(out.ok?'对话完成':'对话部分完成，查看状态');if(out.ok)$('#message').value=''}catch(e){toast(e.message);const b=$('#banner');b.style.display='block';b.innerHTML='<span class="err">'+esc(e.message)+'</span>'}finally{$('#view').classList.remove('busy');if(busy)busy.textContent=''}}
async function refreshAll(){await Promise.all([loadHealth(),loadEnvs(),loadConversations(),loadApprovals()])}
(function init(){const hm=location.hash.match(/^#token=(.+)$/);if(hm){localStorage.setItem('lanbus_token',decodeURIComponent(hm[1]));history.replaceState(null,'',location.pathname)}const saved=localStorage.getItem('lanbus_token');if(saved)$('#token').value=saved;loadHealth();if(token())refreshAll();else setTimeout(()=>{if(token())refreshAll()},1200);setInterval(loadHealth,10000)})();
</script>
</body>
</html>
"""
