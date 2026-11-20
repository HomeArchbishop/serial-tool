import { Events } from '@wailsio/runtime'
import { useEffect, useRef, useState } from 'react'

import { TunnelService } from '../bindings/serial-tool'
import type { Activity, SSHHost, ToolStatus, TunnelConfig } from '../bindings/serial-tool/models'

const DEFAULT_REMOTE = '/dev/ttyACM0'
const emptyActivity: Activity = { running: false, listening: false, linked: false, message: '未连接' }

export default function App () {
  const [devices, setDevices] = useState<string[]>([])
  const [device, setDevice] = useState('')
  const [hosts, setHosts] = useState<SSHHost[]>([])
  const [keys, setKeys] = useState<string[]>([])
  const [hostPick, setHostPick] = useState('')
  const [manual, setManual] = useState(false)
  const [host, setHost] = useState('')
  const [user, setUser] = useState('')
  const [password, setPassword] = useState('')
  const [identityFile, setIdentityFile] = useState('')
  const [sshPort, setSSHPort] = useState(22)
  const [linkName, setLinkName] = useState(DEFAULT_REMOTE)
  const [sudoPassword, setSudoPassword] = useState('')
  const [askSudo, setAskSudo] = useState(false)
  const [sudoDraft, setSudoDraft] = useState('')
  const [tunnelPort, setTunnelPort] = useState(47329)
  const [running, setRunning] = useState(false)
  const [busy, setBusy] = useState(false)
  const [localSocat, setLocalSocat] = useState<ToolStatus | null>(null)
  const [activity, setActivity] = useState<Activity>(emptyActivity)
  const [logs, setLogs] = useState<string[]>([])
  const logRef = useRef<HTMLPreElement>(null)
  const sudoRef = useRef<HTMLInputElement>(null)
  const locked = running || busy

  const applyHost = (alias: string, list: SSHHost[] = hosts) => {
    setHostPick(alias)
    if (!alias) {
      setManual(true)
      return
    }
    setManual(false)
    const h = list.find((x) => x.alias === alias)
    if (!h) return
    setHost(h.host || h.alias)
    setUser(h.user || '')
    setSSHPort(h.port || 22)
    setIdentityFile(h.identityFile || '')
    setPassword('')
  }

  const refreshDevices = async () => {
    const list = await TunnelService.ListDevices() ?? []
    setDevices(list)
    setDevice((cur) => (cur && list.includes(cur) ? cur : list[0] ?? ''))
  }

  const logLines = (...lines: string[]) => {
    setLogs((xs) => [...xs, ...lines])
  }

  const logToolBlock = (scope: string, s: ToolStatus, copyTip = false) => {
    const lines = [`── ${scope} ──`, s.message, `安装命令: ${s.hint}`]
    if (copyTip) lines.push('点上方红色提示可复制安装命令')
    logLines(...lines)
  }

  const copyHint = async (hint: string) => {
    try {
      await navigator.clipboard.writeText(hint)
      logLines(`已复制: ${hint}`)
    } catch {
      logLines(`复制失败，请手动执行: ${hint}`)
    }
  }

  const refreshSSH = async () => {
    const [h, k, socat] = await Promise.all([
      TunnelService.ListSSHHosts(),
      TunnelService.ListSSHKeys(),
      TunnelService.CheckLocalSocat(),
    ])
    setHosts(h ?? [])
    setKeys(k ?? [])
    setLocalSocat(socat)
    if (socat && !socat.ok) {
      logToolBlock('本机 socat', socat, true)
    }
    if (!(h?.length)) {
      setManual(true)
      setHostPick('')
    } else if (!hostPick) {
      applyHost(h[0].alias, h)
    }
  }

  const cfg = (sudo = sudoPassword): TunnelConfig => ({
    device,
    host: host.trim(),
    user: user.trim(),
    password,
    identityFile: identityFile.trim(),
    sshPort: Number(sshPort) || 22,
    tunnelPort: Number(tunnelPort) || 47329,
    linkName: linkName.trim() || DEFAULT_REMOTE,
    sudoPassword: sudo,
  })

  const startWith = async (sudo: string) => {
    const local = await TunnelService.CheckLocalSocat()
    setLocalSocat(local)
    if (!local.ok) {
      logToolBlock('本机 socat 缺失', local, true)
      return
    }
    const c = cfg(sudo)
    const remote = await TunnelService.CheckRemoteSocat(c)
    if (!remote.ok) {
      logToolBlock('远程 socat 缺失', remote)
      return
    }
    await TunnelService.Start(c)
  }

  const run = async () => {
    if (busy || askSudo) return
    setBusy(true)
    try {
      if (running) {
        await TunnelService.Stop()
        return
      }
      await startWith(sudoPassword)
    } catch (err) {
      if (String(err).includes('SUDO_PASSWORD_REQUIRED')) {
        setSudoDraft('')
        setAskSudo(true)
        setLogs((xs) => [...xs, '远端需要 sudo 密码'])
        return
      }
      setLogs((xs) => [...xs, String(err)])
    } finally {
      setBusy(false)
    }
  }

  const submitSudo = async () => {
    if (!sudoDraft) return
    setAskSudo(false)
    setSudoPassword(sudoDraft)
    setBusy(true)
    try {
      await startWith(sudoDraft)
    } catch (err) {
      if (String(err).includes('SUDO_PASSWORD_REQUIRED')) {
        setAskSudo(true)
        setLogs((xs) => [...xs, 'sudo 密码不正确'])
        return
      }
      setLogs((xs) => [...xs, String(err)])
    } finally {
      setBusy(false)
    }
  }

  useEffect(() => {
    refreshDevices().catch(() => {})
    refreshSSH().catch(() => {})
    TunnelService.IsRunning().then(setRunning).catch(() => {})
    TunnelService.GetActivity().then(setActivity).catch(() => {})
    const offLog = Events.On('tunnel:log', (e) => setLogs((xs) => [...xs, String(e.data ?? '')]))
    const offStatus = Events.On('tunnel:status', (e) => setRunning(Boolean(e.data)))
    const offAct = Events.On('tunnel:activity', (e) => setActivity((e.data as Activity) ?? emptyActivity))
    return () => { offLog(); offStatus(); offAct() }
  }, [])

  useEffect(() => { logRef.current?.scrollTo(0, logRef.current.scrollHeight) }, [logs])
  useEffect(() => { if (askSudo) sudoRef.current?.focus() }, [askSudo])

  const runRef = useRef(run)
  runRef.current = run
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (askSudo || e.key !== 'Enter' || e.metaKey || e.ctrlKey) return
      if ((e.target as HTMLElement)?.tagName === 'TEXTAREA') return
      e.preventDefault()
      runRef.current().catch(() => {})
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [askSudo])

  const act = !running ? 'off' : activity.linked ? 'on' : activity.listening ? 'wait' : 'off'
  const picked = hosts.find((h) => h.alias === hostPick)

  return (
    <div className='app'>
      <div className='drag' />
      <main className='panel'>
        <div className='toolbar'>
          <div className={`status ${act}`}>
            <i />
            <span>{running ? activity.message : '未连接'}</span>
          </div>
          <button
            type='button'
            className={running ? 'danger' : 'primary'}
            disabled={busy || askSudo || (!running && !device)}
            onClick={() => { run().catch(() => {}) }}
          >
            {busy ? '…' : running ? '停止' : '启动'}
          </button>
        </div>

        {!localSocat?.ok && localSocat !== null && (
          <button
            type='button'
            className='note'
            title='点击复制安装命令'
            onClick={() => { copyHint(localSocat.hint).catch(() => {}) }}
          >
            <span>{localSocat.message}</span>
            <code>{localSocat.hint}</code>
            <em>点击复制</em>
          </button>
        )}

        <label className='row'>
          <span>串口</span>
          <div className='controls'>
            <select value={device} disabled={locked} onChange={(e) => setDevice(e.target.value)}>
              {devices.length === 0 && <option value=''>无串口</option>}
              {devices.map((d) => <option key={d} value={d}>{d}</option>)}
            </select>
            <button type='button' className='tiny' disabled={locked} onClick={() => { refreshDevices().catch(() => {}) }}>刷新</button>
          </div>
        </label>

        <label className='row'>
          <span>SSH</span>
          <div className='controls'>
            <select
              value={manual ? '' : hostPick}
              disabled={locked}
              onChange={(e) => {
                const v = e.target.value
                if (!v) { setManual(true); setHostPick(''); return }
                applyHost(v)
              }}
            >
              {hosts.map((h) => <option key={h.alias} value={h.alias}>{h.alias}</option>)}
              <option value=''>手动填写…</option>
            </select>
            <button type='button' className='tiny' disabled={locked} onClick={() => { refreshSSH().catch(() => {}) }}>刷新</button>
          </div>
        </label>

        {!manual && picked && (
          <div className='hint'>
            {picked.user ? `${picked.user}@` : ''}{picked.host || picked.alias}
            {picked.port && picked.port !== 22 ? `:${picked.port}` : ''}
          </div>
        )}

        {manual && (
          <div className='manual'>
            <label className='row'>
              <span>主机</span>
              <input type='text' value={host} disabled={locked} onChange={(e) => setHost(e.target.value)} placeholder='ip / hostname' spellCheck={false} />
            </label>
            <div className='row'>
              <span>账号</span>
              <div className='pair'>
                <input type='text' value={user} disabled={locked} onChange={(e) => setUser(e.target.value)} placeholder='ubuntu' />
                <input type='number' value={sshPort} disabled={locked} onChange={(e) => setSSHPort(Number(e.target.value) || 22)} aria-label='SSH 端口' />
              </div>
            </div>
            <label className='row'>
              <span>密码</span>
              <input type='password' value={password} disabled={locked} onChange={(e) => setPassword(e.target.value)} placeholder='可选' />
            </label>
            <label className='row'>
              <span>私钥</span>
              <select value={identityFile} disabled={locked} onChange={(e) => setIdentityFile(e.target.value)}>
                <option value=''>agent / 默认</option>
                {keys.map((k) => <option key={k} value={k}>{k.replace(/^.*\//, '')}</option>)}
              </select>
            </label>
          </div>
        )}

        <label className='row'>
          <span>远程串口</span>
          <input type='text' value={linkName} disabled={locked} onChange={(e) => setLinkName(e.target.value)} placeholder={DEFAULT_REMOTE} spellCheck={false} />
        </label>

        <details className='advanced'>
          <summary>高级</summary>
          <label className='row'>
            <span>隧道端口</span>
            <input
              type='number'
              value={tunnelPort}
              disabled={locked}
              onChange={(e) => setTunnelPort(Number(e.target.value) || 47329)}
            />
          </label>
        </details>

        <pre className='log' ref={logRef}>{logs.join('\n') || ' '}</pre>
      </main>

      {askSudo && (
        <div className='modal-bg'>
          <div className='modal'>
            <h2>需要 sudo 密码</h2>
            <p>远端免密不可用，请输入密码以创建虚拟串口。</p>
            <input
              ref={sudoRef}
              type='password'
              value={sudoDraft}
              onChange={(e) => setSudoDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') submitSudo().catch(() => {})
                if (e.key === 'Escape') setAskSudo(false)
              }}
              placeholder='sudo 密码'
            />
            <div className='modal-actions'>
              <button type='button' onClick={() => setAskSudo(false)}>取消</button>
              <button type='button' className='primary' disabled={!sudoDraft || busy} onClick={() => { submitSudo().catch(() => {}) }}>继续</button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
