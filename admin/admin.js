const $ = (id) => document.getElementById(id)
const request = async (path, options = {}) => {
  const response = await fetch(path, { credentials: 'same-origin', ...options })
  if (!response.ok) {
    const body = await response.json().catch(() => ({}))
    throw new Error(body.error?.message || `请求失败 (${response.status})`)
  }
  return response.status === 204 ? null : response.json()
}
const showLogin = () => { $('login').classList.remove('hidden'); $('config').classList.add('hidden') }
const showConfig = (config) => {
  $('login').classList.add('hidden'); $('config').classList.remove('hidden')
  for (const key of ['base_url', 'temperature']) $(key).value = config[key] ?? ''
  $('model').replaceChildren(new Option(config.model || 'gpt-5.3-codex', config.model || 'gpt-5.3-codex'))
  $('api_key').value = ''; $('key-status').textContent = config.api_key_set ? '已保存；留空不会覆盖' : '尚未配置'
  $('access_password').value = ''; $('access-status').textContent = config.access_password_set ? '已设置；留空不会覆盖' : '尚未设置，前台无法使用'
}
const load = async () => { try { showConfig(await request('/api/admin/config')) } catch { showLogin() } }
$('login-form').addEventListener('submit', async (event) => {
  event.preventDefault(); $('login-error').textContent = ''
  try { await request('/api/admin/login', { method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify({password:$('password').value}) }); $('password').value=''; await load() }
  catch (error) { $('login-error').textContent = error.message }
})
$('config-form').addEventListener('submit', async (event) => {
  event.preventDefault(); $('status').className=''; $('status').textContent='保存中…'; $('save').disabled=true
  const body = {}; for (const key of ['base_url','api_key','model','access_password']) body[key]=$(key).value; body.temperature=Number($('temperature').value)
  try { await request('/api/admin/config',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)}); $('status').className='ok';$('status').textContent='保存成功';await load() }
  catch(error){$('status').textContent=error.message} finally {$('save').disabled=false}
})
$('load-models').addEventListener('click', async () => {
  $('model-status').textContent='获取中…'; $('load-models').disabled=true
  try {
    const result=await request('/api/admin/models',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({base_url:$('base_url').value,api_key:$('api_key').value})})
    const current=$('model').value
    $('model').replaceChildren(...result.models.map((name)=>new Option(name,name)))
    if(result.models.includes(current)) $('model').value=current
    $('model-status').textContent=`已获取 ${result.models.length} 个模型`
  } catch(error) { $('model-status').textContent=error.message } finally { $('load-models').disabled=false }
})
$('logout').addEventListener('click', async()=>{await request('/api/admin/logout',{method:'POST'}).catch(()=>{});showLogin()})
load()
