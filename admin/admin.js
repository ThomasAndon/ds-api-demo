const $ = (id) => document.getElementById(id)
const request = async (path, options = {}) => {
  const response = await fetch(path, { credentials: 'same-origin', ...options })
  if (!response.ok) { const body=await response.json().catch(()=>({})); throw new Error(body.error?.message||`请求失败 (${response.status})`) }
  return response.status===204?null:response.json()
}
let profiles=[]
const showLogin=()=>{$('login').classList.remove('hidden');$('config').classList.add('hidden')}
const showConfig=()=>{$('login').classList.add('hidden');$('config').classList.remove('hidden')}
const actionButton=(label,handler,className='')=>{const button=document.createElement('button');button.type='button';button.textContent=label;button.className=className;button.addEventListener('click',handler);return button}
const renderProfiles=()=>{
  $('profiles').replaceChildren(...profiles.map((profile)=>{
    const card=document.createElement('article');card.className=`profile${profile.active?' active':''}`
    const head=document.createElement('div');head.className='profile-head';const name=document.createElement('span');name.className='profile-name';name.textContent=profile.name;head.append(name)
    if(profile.active){const badge=document.createElement('span');badge.className='badge';badge.textContent='当前使用';head.append(badge)}
    const meta=document.createElement('div');meta.className='profile-meta'
    for(const text of [profile.base_url,`模型：${profile.model}`,`温度：${profile.temperature} · API Key：${profile.api_key_set?'已保存':'未配置'}`]){const line=document.createElement('span');line.textContent=text;meta.append(line)}
    const actions=document.createElement('div');actions.className='profile-actions'
    if(!profile.active)actions.append(actionButton('切换使用',()=>activate(profile.id)))
    actions.append(actionButton('编辑',()=>editProfile(profile),'secondary'))
    if(!profile.active)actions.append(actionButton('删除',()=>removeProfile(profile),'danger'))
    card.append(head,meta,actions);return card
  }))
}
const load=async()=>{try{const [items,config]=await Promise.all([request('/api/admin/profiles'),request('/api/admin/config')]);profiles=items;renderProfiles();showConfig();$('access-status').textContent=config.access_password_set?'已设置；填写新值才会修改':'尚未设置'}catch{showLogin()}}
const resetEditor=()=>{$('profile-form').reset();$('profile_id').value='';$('base_url').value='https://la-api.vxvxvx.top/v1';$('temperature').value='0.7';$('model').replaceChildren(new Option('gpt-5.3-codex','gpt-5.3-codex'));$('key-status').textContent='新增凭据必须填写';$('model-status').textContent='填写 URL 和 API Key 后可获取模型。';$('profile-status').textContent='';$('editor-heading').textContent='新增凭据'}
const editProfile=(profile)=>{resetEditor();$('profile_id').value=String(profile.id);$('name').value=profile.name;$('base_url').value=profile.base_url;$('temperature').value=String(profile.temperature);$('model').replaceChildren(new Option(profile.model,profile.model));$('key-status').textContent=profile.api_key_set?'已保存；留空不会覆盖':'尚未配置';$('editor-heading').textContent=`编辑：${profile.name}`;$('profile-form').classList.remove('hidden');$('name').focus()}
const activate=async(id)=>{$('list-status').textContent='切换中…';try{await request(`/api/admin/profiles/${id}/activate`,{method:'POST'});$('list-status').textContent='切换成功';await load()}catch(error){$('list-status').textContent=error.message}}
const removeProfile=async(profile)=>{if(!confirm(`确定删除“${profile.name}”吗？`))return;try{await request(`/api/admin/profiles/${profile.id}`,{method:'DELETE'});await load()}catch(error){$('list-status').textContent=error.message}}
$('login-form').addEventListener('submit',async(event)=>{event.preventDefault();$('login-error').textContent='';try{await request('/api/admin/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({password:$('password').value})});$('password').value='';await load()}catch(error){$('login-error').textContent=error.message}})
$('add-profile').addEventListener('click',()=>{resetEditor();$('profile-form').classList.remove('hidden');$('name').focus()})
$('cancel-edit').addEventListener('click',()=>$('profile-form').classList.add('hidden'))
$('profile-form').addEventListener('submit',async(event)=>{event.preventDefault();$('profile-status').className='';$('profile-status').textContent='保存中…';$('save-profile').disabled=true;const id=$('profile_id').value;const body={name:$('name').value,base_url:$('base_url').value,api_key:$('api_key').value,model:$('model').value,temperature:Number($('temperature').value)};try{await request(id?`/api/admin/profiles/${id}`:'/api/admin/profiles',{method:id?'PUT':'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});$('profile-status').className='ok';$('profile-status').textContent='保存成功';$('profile-form').classList.add('hidden');await load()}catch(error){$('profile-status').textContent=error.message}finally{$('save-profile').disabled=false}})
$('load-models').addEventListener('click',async()=>{$('model-status').textContent='获取中…';$('load-models').disabled=true;try{const result=await request('/api/admin/models',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({profile_id:Number($('profile_id').value)||0,base_url:$('base_url').value,api_key:$('api_key').value})});const current=$('model').value;$('model').replaceChildren(...result.models.map((name)=>new Option(name,name)));if(result.models.includes(current))$('model').value=current;$('model-status').textContent=`已获取 ${result.models.length} 个模型`}catch(error){$('model-status').textContent=error.message}finally{$('load-models').disabled=false}})
$('access-form').addEventListener('submit',async(event)=>{event.preventDefault();const password=$('access_password').value.trim();if(!password)return;$('access-status').className='';$('access-status').textContent='保存中…';try{await request('/api/admin/access',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify({password})});$('access_password').value='';$('access-status').className='ok';$('access-status').textContent='保存成功'}catch(error){$('access-status').textContent=error.message}})
$('logout').addEventListener('click',async()=>{await request('/api/admin/logout',{method:'POST'}).catch(()=>{});showLogin()})
load()
