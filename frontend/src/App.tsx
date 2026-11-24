import { Events } from '@wailsio/runtime'
import { useEffect, useRef, useState } from 'react'

import { DeviceService } from '../bindings/serial-tool'
import type { Activity, CameraDevice, DepthCamera, DeviceConfig, DeviceState, SSHHost, ToolStatus } from '../bindings/serial-tool/models'

const KIND_ARM = 'arm'
const KIND_CAMERA = 'camera'
const MODEL_RGB = 'rgb'
const MODEL_ORBBEC = 'orbbec'

type DeviceKind = typeof KIND_ARM | typeof KIND_CAMERA

interface DeviceLogEvent { id: string, line: string }
interface DeviceStatusEvent { id: string, running: boolean }
interface DeviceActivityEvent { id: string, activity: Activity }

export default function App () {
  const [devices, setDevices] = useState<DeviceState[]>([])
  const [drafts, setDrafts] = useState<Record<string, DeviceConfig>>({})
  const [activeId, setActiveId] = useState('')
  const [busy, setBusy] = useState<Record<string, boolean>>({})
  const [sudoId, setSudoId] = useState<string | null>(null)
  const [sudoDraft, setSudoDraft] = useState('')
  const [addOpen, setAddOpen] = useState(false)

  const [hosts, setHosts] = useState<SSHHost[]>([])
  const [keys, setKeys] = useState<string[]>([])
  const [manualIds, setManualIds] = useState<Record<string, boolean>>({})
  const [serialPorts, setSerialPorts] = useState<string[]>([])
  const [cameras, setCameras] = useState<CameraDevice[]>([])
  const [depthCameras, setDepthCameras] = useState<DepthCamera[]>([])
  const [depthError, setDepthError] = useState('')
  const [localSocat, setLocalSocat] = useState<ToolStatus | null>(null)
  const [localFFmpeg, setLocalFFmpeg] = useState<ToolStatus | null>(null)
  const [localDepthHelper, setLocalDepthHelper] = useState<ToolStatus | null>(null)

  const [logs, setLogs] = useState<string[]>([])
  const logRef = useRef<HTMLPreElement>(null)
  const sudoRef = useRef<HTMLInputElement>(null)
  const devicesRef = useRef<DeviceState[]>([])
  devicesRef.current = devices

  const isOrbbec = (cfg: DeviceConfig) => cfg.kind === KIND_CAMERA && cfg.cameraModel === MODEL_ORBBEC
  const active = devices.find((d) => d.config.id === activeId) ?? null
  const draft = active ? drafts[active.config.id] ?? active.config : null
  const running = active?.running ?? false
  const busyNow = active ? Boolean(busy[active.config.id]) : false
  const locked = running || busyNow
  const kind: DeviceKind = (active?.config.kind as DeviceKind) ?? KIND_ARM

  const logLines = (...lines: string[]) => {
    setLogs((xs) => [...xs, ...lines])
  }

  const logToolBlock = (scope: string, s: ToolStatus) => {
    logLines(`── ${scope} ──`, s.message, `安装命令: ${s.hint}`)
  }

  const copyHint = async (hint: string) => {
    try {
      await navigator.clipboard.writeText(hint)
      logLines(`已复制: ${hint}`)
    } catch {
      logLines(`复制失败，请手动执行: ${hint}`)
    }
  }

  const setDeviceState = (state: DeviceState) => {
    setDevices((xs) => xs.map((d) => (d.config.id === state.config.id ? state : d)))
    setDrafts((ds) => ({ ...ds, [state.config.id]: state.config }))
  }

  const setBusyFor = (id: string, value: boolean) => {
    setBusy((b) => ({ ...b, [id]: value }))
  }

  const updateDraft = (patch: Partial<DeviceConfig>) => {
    if (!active) return
    const current = drafts[active.config.id] ?? active.config
    setDrafts((ds) => ({ ...ds, [active.config.id]: { ...current, ...patch } }))
  }

  const applyHost = (alias: string) => {
    if (!alias) {
      if (active) setManualIds((ms) => ({ ...ms, [active.config.id]: true }))
      return
    }
    const h = hosts.find((x) => x.alias === alias)
    if (!h) return
    if (active) setManualIds((ms) => ({ ...ms, [active.config.id]: false }))
    updateDraft({
      host: h.host || h.alias,
      user: h.user || '',
      sshPort: h.port || 22,
      identityFile: h.identityFile || '',
      password: '',
    })
  }

  const refreshSSH = async () => {
    const [h, k, socat, ffmpeg, depth] = await Promise.all([
      DeviceService.ListSSHHosts(),
      DeviceService.ListSSHKeys(),
      DeviceService.CheckLocalSocat(),
      DeviceService.CheckLocalFFmpeg(),
      DeviceService.CheckDepthHelper(),
    ])
    setHosts(h ?? [])
    setKeys(k ?? [])
    setLocalSocat(socat)
    setLocalFFmpeg(ffmpeg)
    setLocalDepthHelper(depth)
  }

  const refreshDepthCameras = async () => {
    let result: Awaited<ReturnType<typeof DeviceService.ListDepthCamerasDetailed>>
    try {
      result = await DeviceService.ListDepthCamerasDetailed()
    } catch (err) {
      setDepthError(`枚举失败: ${String(err)}`)
      return
    }
    const list = result?.devices ?? []
    setDepthCameras(list)
    setDepthError(result?.error ?? '')
    if (active && isOrbbec(drafts[active.config.id] ?? active.config)) {
      const cur = drafts[active.config.id] ?? active.config
      if (cur.depthSerial && list.some((c) => c.id === cur.depthSerial)) return
      updateDraft({ depthSerial: list[0]?.id ?? '', depthName: list[0]?.name ?? '' })
    }
  }

  const refreshSerialPorts = async () => {
    const list = await DeviceService.ListSerialPorts() ?? []
    setSerialPorts(list)
    if (active && (drafts[active.config.id] ?? active.config).kind === KIND_ARM) {
      const cur = drafts[active.config.id] ?? active.config
      if (cur.serialDevice && list.includes(cur.serialDevice)) return
      updateDraft({ serialDevice: list[0] ?? '' })
    }
  }

  const refreshCameras = async () => {
    const list = await DeviceService.ListCameras() ?? []
    setCameras(list)
    if (active && (drafts[active.config.id] ?? active.config).kind === KIND_CAMERA) {
      const cur = drafts[active.config.id] ?? active.config
      if (cur.cameraId && list.some((c) => c.id === cur.cameraId)) return
      updateDraft({ cameraId: list[0]?.id ?? '', cameraName: list[0]?.name ?? '' })
    }
  }

  const addDevice = async (deviceKind: DeviceKind) => {
    const base = draft
    const seed: DeviceConfig = {
      id: '',
      kind: deviceKind,
      cameraModel: MODEL_RGB,
      label: '',
      host: base?.host ?? '',
      user: base?.user ?? '',
      password: '',
      identityFile: base?.identityFile ?? '',
      sshPort: base?.sshPort ?? 22,
      sudoPassword: '',
      serialDevice: serialPorts[0] ?? '',
      linkName: '/dev/ttyACM0',
      tunnelPort: 0,
      cameraId: cameras[0]?.id ?? '',
      cameraName: cameras[0]?.name ?? '',
      remoteDevice: '',
      port: 0,
      width: 0,
      height: 0,
      fps: 0,
      depthSerial: depthCameras[0]?.id ?? '',
      depthName: depthCameras[0]?.name ?? '',
      depthWidth: 640,
      depthHeight: 400,
      depthFps: 30,
      depthPort: 0,
      depthAlign: 'color',
    }
    const state = await DeviceService.AddDevice(seed)
    setDevices((xs) => [...xs, state])
    setDrafts((ds) => ({ ...ds, [state.config.id]: state.config }))
    setActiveId(state.config.id)
    setAddOpen(false)
  }

  const removeDevice = async (state: DeviceState) => {
    if (state.running && !window.confirm(`删除「${state.config.label}」？正在运行的连接会停止。`)) return
    await DeviceService.RemoveDevice(state.config.id)
    const rest = devices.filter((d) => d.config.id !== state.config.id)
    setDevices(rest)
    setDrafts((ds) => {
      const next = { ...ds }
      delete next[state.config.id]
      return next
    })
    if (activeId === state.config.id) setActiveId(rest[0]?.config.id ?? '')
  }

  const handleError = (id: string, err: unknown) => {
    const msg = String(err)
    if (msg.includes('SUDO_PASSWORD_REQUIRED')) {
      setSudoDraft('')
      setSudoId(id)
      logLines('远端需要 sudo 密码')
      return
    }
    logLines(msg)
  }

  const runDevice = async (state: DeviceState) => {
    const id = state.config.id
    if (busy[id] || sudoId) return
    const cfg = drafts[id] ?? state.config
    setBusyFor(id, true)
    try {
      if (state.running) {
        await DeviceService.StopDevice(id)
        return
      }
      let local: ToolStatus
      let localScope = '本机 socat 缺失'
      if (cfg.kind === KIND_CAMERA) {
        local = await DeviceService.CheckLocalFFmpeg()
        setLocalFFmpeg(local)
        localScope = '本机 ffmpeg 缺失'
        if (isOrbbec(cfg)) {
          const helperStatus = await DeviceService.CheckDepthHelper()
          setLocalDepthHelper(helperStatus)
          if (!helperStatus.ok) {
            logToolBlock('深度助手缺失', helperStatus)
            return
          }
        }
      } else {
        local = await DeviceService.CheckLocalSocat()
        setLocalSocat(local)
      }
      if (!local.ok) {
        logToolBlock(localScope, local)
        return
      }
      const remote = await DeviceService.CheckRemoteDevice(cfg)
      if (!remote.ok) {
        logToolBlock(cfg.kind === KIND_CAMERA ? '远程推流环境缺失' : '远程连接检查失败', remote)
        return
      }
      const next = await DeviceService.StartDevice(cfg)
      setDeviceState(next)
    } catch (err) {
      handleError(id, err)
    } finally {
      setBusyFor(id, false)
    }
  }

  const submitSudo = async () => {
    const id = sudoId
    if (!id || !sudoDraft) return
    const state = devices.find((d) => d.config.id === id)
    if (!state) {
      setSudoId(null)
      return
    }
    const cfg = { ...(drafts[id] ?? state.config), sudoPassword: sudoDraft }
    setSudoId(null)
    setBusyFor(id, true)
    try {
      const next = await DeviceService.StartDevice(cfg)
      setDeviceState(next)
    } catch (err) {
      const msg = String(err)
      if (msg.includes('SUDO_PASSWORD_REQUIRED')) {
        setSudoId(id)
        logLines('sudo 密码不正确')
        return
      }
      logLines(msg)
    } finally {
      setBusyFor(id, false)
    }
  }

  useEffect(() => {
    DeviceService.ListDevices().then((list) => {
      const ls = list ?? []
      setDevices(ls)
      setDrafts(Object.fromEntries(ls.map((d) => [d.config.id, d.config])))
      setActiveId((cur) => (cur && ls.some((d) => d.config.id === cur) ? cur : ls[0]?.config.id ?? ''))
    }).catch(() => {})
    refreshSSH().catch(() => {})
    refreshSerialPorts().catch(() => {})
    refreshCameras().catch(() => {})
    refreshDepthCameras().catch(() => {})

    const offLog = Events.On('device:log', (e) => {
      const p = e.data as DeviceLogEvent
      const label = devicesRef.current.find((d) => d.config.id === p.id)?.config.label ?? p.id
      setLogs((xs) => [...xs, `[${label}] ${p.line}`])
    })
    const offStatus = Events.On('device:status', (e) => {
      const p = e.data as DeviceStatusEvent
      setDevices((xs) => xs.map((d) => (d.config.id === p.id ? { ...d, running: p.running } : d)))
    })
    const offAct = Events.On('device:activity', (e) => {
      const p = e.data as DeviceActivityEvent
      setDevices((xs) => xs.map((d) => (d.config.id === p.id ? { ...d, activity: p.activity } : d)))
    })
    return () => { offLog(); offStatus(); offAct() }
  }, [])

  useEffect(() => { logRef.current?.scrollTo(0, logRef.current.scrollHeight) }, [logs])
  useEffect(() => { if (sudoId) sudoRef.current?.focus() }, [sudoId])

  const runRef = useRef(runDevice)
  runRef.current = runDevice
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (sudoId || addOpen || e.key !== 'Enter' || e.metaKey || e.ctrlKey) return
      if ((e.target as HTMLElement)?.tagName === 'TEXTAREA') return
      if (!active) return
      e.preventDefault()
      runRef.current(active).catch(() => {})
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [sudoId, addOpen, active])

  const sshAlias = draft
    ? hosts.find((h) => h.host === draft.host && (h.user || '') === (draft.user || ''))?.alias ?? ''
    : ''
  const manual = active ? manualIds[active.config.id] ?? (sshAlias === '') : false
  const statusClass = !running ? 'off' : active?.activity.linked ? 'on' : active?.activity.listening ? 'wait' : 'off'
  const pickedCamera = cameras.find((c) => c.id === draft?.cameraId)

  return (
    <div className='app'>
      <div className='drag' />
      <main className='panel'>
        <div className='tabs'>
          {devices.map((d) => (
            <button
              key={d.config.id}
              type='button'
              className={d.config.id === activeId ? 'on' : ''}
              onClick={() => setActiveId(d.config.id)}
              title={d.config.label}
            >
              {d.config.label}{d.running && <i className='tab-dot' />}
            </button>
          ))}
          <button type='button' className='add' onClick={() => setAddOpen(true)}>＋</button>
        </div>

        {!active && (
          <div className='empty'>
            <p>还没有设备</p>
            <button type='button' className='primary' onClick={() => setAddOpen(true)}>添加设备</button>
          </div>
        )}

        {active && draft && (
          <>
            <div className='toolbar'>
              <div className={`status ${statusClass}`}>
                <i />
                <span>{running ? active.activity.message : kind === KIND_CAMERA ? '未推流' : '未连接'}</span>
              </div>
              <div className='actions'>
                <button type='button' className='tiny bad' disabled={busyNow} onClick={() => { removeDevice(active).catch(() => {}) }}>删除</button>
                <button
                  type='button'
                  className={running ? 'danger' : 'primary'}
                  disabled={busyNow || Boolean(sudoId)}
                  onClick={() => { runDevice(active).catch(() => {}) }}
                >
                  {busyNow ? '…' : running ? '停止' : '启动'}
                </button>
              </div>
            </div>

            {kind === KIND_ARM && localSocat && !localSocat.ok && (
              <button type='button' className='note' title='点击复制安装命令' onClick={() => { copyHint(localSocat.hint).catch(() => {}) }}>
                <span>{localSocat.message}</span>
                <code>{localSocat.hint}</code>
                <em>点击复制</em>
              </button>
            )}
            {kind === KIND_CAMERA && localFFmpeg && !localFFmpeg.ok && (
              <button type='button' className='note' title='点击复制安装命令' onClick={() => { copyHint(localFFmpeg.hint).catch(() => {}) }}>
                <span>{localFFmpeg.message}</span>
                <code>{localFFmpeg.hint}</code>
                <em>点击复制</em>
              </button>
            )}
            {kind === KIND_CAMERA && isOrbbec(draft) && localDepthHelper && !localDepthHelper.ok && (
              <button type='button' className='note' title='点击复制安装命令' onClick={() => { copyHint(localDepthHelper.hint).catch(() => {}) }}>
                <span>{localDepthHelper.message}</span>
                <code>{localDepthHelper.hint}</code>
                <em>点击复制</em>
              </button>
            )}

            {kind === KIND_ARM && (
              <label className='row'>
                <span>串口</span>
                <div className='controls'>
                  <select value={draft.serialDevice} disabled={locked} onChange={(e) => updateDraft({ serialDevice: e.target.value })}>
                    {serialPorts.length === 0 && <option value=''>无串口</option>}
                    {draft.serialDevice && !serialPorts.includes(draft.serialDevice) && (
                      <option value={draft.serialDevice}>{draft.serialDevice}</option>
                    )}
                    {serialPorts.map((p) => <option key={p} value={p}>{p}</option>)}
                  </select>
                  <button type='button' className='tiny' disabled={locked} onClick={() => { refreshSerialPorts().catch(() => {}) }}>刷新</button>
                </div>
              </label>
            )}

            {kind === KIND_CAMERA && (
              <label className='row'>
                <span>型号</span>
                <select
                  value={draft.cameraModel || MODEL_RGB}
                  disabled={locked}
                  onChange={(e) => {
                    updateDraft({ cameraModel: e.target.value })
                    if (e.target.value === MODEL_ORBBEC) refreshDepthCameras().catch(() => {})
                  }}
                >
                  <option value={MODEL_RGB}>普通 RGB 相机</option>
                  <option value={MODEL_ORBBEC}>Orbbec DaBai（彩色+深度）</option>
                </select>
              </label>
            )}

            {kind === KIND_CAMERA && (
              <>
                <label className='row'>
                  <span>相机</span>
                  <div className='controls'>
                    <select
                      value={draft.cameraId} disabled={locked} onChange={(e) => {
                        const cam = cameras.find((c) => c.id === e.target.value)
                        updateDraft({ cameraId: e.target.value, cameraName: cam?.name ?? '' })
                      }}
                    >
                      {cameras.length === 0 && draft.cameraId === '' && <option value=''>无相机</option>}
                      {draft.cameraId && !cameras.some((c) => c.id === draft.cameraId) && (
                        <option value={draft.cameraId}>{draft.cameraName || draft.cameraId}</option>
                      )}
                      {cameras.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
                    </select>
                    <button type='button' className='tiny' disabled={locked} onClick={() => { refreshCameras().catch(() => {}) }}>刷新</button>
                  </div>
                </label>
                {pickedCamera && pickedCamera.name !== pickedCamera.id && (
                  <div className='hint'>输入: {pickedCamera.id}</div>
                )}
              </>
            )}

            {kind === KIND_CAMERA && isOrbbec(draft) && (
              <>
                <label className='row'>
                  <span>深度相机</span>
                  <div className='controls'>
                    <select
                      value={draft.depthSerial} disabled={locked} onChange={(e) => {
                        const cam = depthCameras.find((c) => c.id === e.target.value)
                        updateDraft({ depthSerial: e.target.value, depthName: cam?.name ?? '' })
                      }}
                    >
                      {depthCameras.length === 0 && draft.depthSerial === '' && <option value=''>无设备</option>}
                      {draft.depthSerial && !depthCameras.some((c) => c.id === draft.depthSerial) && (
                        <option value={draft.depthSerial}>{draft.depthName || draft.depthSerial}</option>
                      )}
                      {depthCameras.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
                    </select>
                    <button type='button' className='tiny' disabled={locked} onClick={() => { refreshDepthCameras().catch(() => {}) }}>刷新</button>
                  </div>
                </label>
                <div className='hint'>
                  {draft.depthSerial
                    ? `ID: ${draft.depthSerial}`
                    : depthError || (depthCameras.length === 0 ? '未发现 Orbbec 深度相机，点刷新重试' : '请选择深度相机')}
                </div>
                {depthError && <div className='hint fixed bad'>{depthError}</div>}
              </>
            )}

            <label className='row'>
              <span>SSH</span>
              <div className='controls'>
                <select
                  value={manual ? '' : sshAlias}
                  disabled={locked}
                  onChange={(e) => applyHost(e.target.value)}
                >
                  {hosts.map((h) => <option key={h.alias} value={h.alias}>{h.alias}</option>)}
                  <option value=''>手动填写…</option>
                </select>
                <button type='button' className='tiny' disabled={locked} onClick={() => { refreshSSH().catch(() => {}) }}>刷新</button>
              </div>
            </label>

            {!manual && draft.host && (
              <div className='hint'>
                {draft.user ? `${draft.user}@` : ''}{draft.host}
                {draft.sshPort && draft.sshPort !== 22 ? `:${draft.sshPort}` : ''}
              </div>
            )}

            {manual && (
              <div className='manual'>
                <label className='row'>
                  <span>主机</span>
                  <input type='text' value={draft.host} disabled={locked} onChange={(e) => updateDraft({ host: e.target.value })} placeholder='ip / hostname' spellCheck={false} />
                </label>
                <div className='row'>
                  <span>账号</span>
                  <div className='pair'>
                    <input type='text' value={draft.user} disabled={locked} onChange={(e) => updateDraft({ user: e.target.value })} placeholder='ubuntu' />
                    <input type='number' value={draft.sshPort} disabled={locked} onChange={(e) => updateDraft({ sshPort: Number(e.target.value) || 22 })} aria-label='SSH 端口' />
                  </div>
                </div>
                <label className='row'>
                  <span>密码</span>
                  <input type='password' value={draft.password} disabled={locked} onChange={(e) => updateDraft({ password: e.target.value })} placeholder='可选' />
                </label>
                <label className='row'>
                  <span>私钥</span>
                  <select value={draft.identityFile} disabled={locked} onChange={(e) => updateDraft({ identityFile: e.target.value })}>
                    <option value=''>agent / 默认</option>
                    {keys.map((k) => <option key={k} value={k}>{k.replace(/^.*\//, '')}</option>)}
                  </select>
                </label>
              </div>
            )}

            {kind === KIND_ARM && (
              <label className='row'>
                <span>远程串口</span>
                <input type='text' value={draft.linkName} disabled={locked} onChange={(e) => updateDraft({ linkName: e.target.value })} placeholder='/dev/ttyACM0' spellCheck={false} />
              </label>
            )}

            {kind === KIND_CAMERA && (
              <label className='row'>
                <span>远程设备</span>
                <input type='text' value={draft.remoteDevice} disabled={locked} onChange={(e) => updateDraft({ remoteDevice: e.target.value })} placeholder='/dev/video10' spellCheck={false} />
              </label>
            )}

            <details className='advanced'>
              <summary>高级</summary>
              {kind === KIND_ARM && (
                <label className='row'>
                  <span>隧道端口</span>
                  <input
                    type='number'
                    value={draft.tunnelPort}
                    disabled={locked}
                    onChange={(e) => updateDraft({ tunnelPort: Number(e.target.value) || 0 })}
                  />
                </label>
              )}
              {kind === KIND_CAMERA && (
                <>
                  <label className='row'>
                    <span>推流端口</span>
                    <input
                      type='number'
                      value={draft.port}
                      disabled={locked}
                      onChange={(e) => updateDraft({ port: Number(e.target.value) || 0 })}
                    />
                  </label>
                  <div className='hint fixed'>H.264 640×480 上限，按相机能力自动协商</div>
                </>
              )}
              {kind === KIND_CAMERA && isOrbbec(draft) && (
                <>
                  <div className='row'>
                    <span>分辨率</span>
                    <div className='pair'>
                      <input
                        type='number' value={draft.depthWidth} disabled={locked}
                        onChange={(e) => updateDraft({ depthWidth: Number(e.target.value) || 0 })}
                        aria-label='深度宽度'
                      />
                      <input
                        type='number' value={draft.depthHeight} disabled={locked}
                        onChange={(e) => updateDraft({ depthHeight: Number(e.target.value) || 0 })}
                        aria-label='深度高度'
                      />
                    </div>
                  </div>
                  <label className='row'>
                    <span>帧率</span>
                    <input
                      type='number' value={draft.depthFps} disabled={locked}
                      onChange={(e) => updateDraft({ depthFps: Number(e.target.value) || 0 })}
                    />
                  </label>
                  <label className='row'>
                    <span>对齐</span>
                    <select value={draft.depthAlign || 'color'} disabled={locked} onChange={(e) => updateDraft({ depthAlign: e.target.value })}>
                      <option value='color'>对齐到彩色（推荐）</option>
                      <option value='none'>原始深度</option>
                    </select>
                  </label>
                  <label className='row'>
                    <span>深度端口</span>
                    <input
                      type='number' value={draft.depthPort} disabled={locked}
                      onChange={(e) => updateDraft({ depthPort: Number(e.target.value) || 0 })}
                    />
                  </label>
                  <div className='hint fixed'>协议见 docs/depth-protocol.md；服务器连 127.0.0.1:{draft.depthPort}</div>
                </>
              )}
            </details>
          </>
        )}

        <pre className='log' ref={logRef}>{logs.join('\n') || ' '}</pre>
      </main>

      {addOpen && (
        <div className='modal-bg'>
          <div className='modal'>
            <h2>添加设备</h2>
            <p>机械臂走虚拟串口；相机走 v4l2loopback，Orbbec 型号额外提供对齐深度流。</p>
            <button type='button' onClick={() => { addDevice(KIND_ARM).catch((err) => logLines(String(err))) }}>机械臂</button>
            <button type='button' onClick={() => { addDevice(KIND_CAMERA).catch((err) => logLines(String(err))) }}>相机</button>
            <div className='modal-actions'>
              <button type='button' onClick={() => setAddOpen(false)}>取消</button>
            </div>
          </div>
        </div>
      )}

      {sudoId && (
        <div className='modal-bg'>
          <div className='modal'>
            <h2>需要 sudo 密码</h2>
            <p>{devices.find((d) => d.config.id === sudoId)?.config.label} 需要 sudo 权限完成远端操作。</p>
            <input
              ref={sudoRef}
              type='password'
              value={sudoDraft}
              onChange={(e) => setSudoDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') submitSudo().catch(() => {})
                if (e.key === 'Escape') setSudoId(null)
              }}
              placeholder='sudo 密码'
            />
            <div className='modal-actions'>
              <button type='button' onClick={() => setSudoId(null)}>取消</button>
              <button type='button' className='primary' disabled={!sudoDraft} onClick={() => { submitSudo().catch(() => {}) }}>继续</button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
