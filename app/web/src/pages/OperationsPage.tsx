import { ArrowRight, Play, RefreshCw, ShieldAlert, Wrench } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { api, APIError } from '../api/client'
import type { OperationDefinition, OperationParameter, OperationParameterField } from '../api/types'
import { Button, EmptyState, ErrorState, IconButton, Loading, PageHeader } from '../components/ui'
import { XrocketReaddressComposerShell, xrocketReaddressOperationID } from '../components/XrocketReaddress'
import { useResource } from '../hooks/useResource'
import { IdempotentOperation } from '../lib/idempotency'
import { operationBlockReason, operationParameterAdvanced, operationParameterLabel, operationPresentation } from '../lib/operationPresentation'

type FormValue = string | boolean
type FormValues = Record<string, FormValue>

export function OperationsPage({ navigate }: { navigate: (path: string) => void }) {
  const resource = useResource(async (signal) => {
    const [catalog, nodes] = await Promise.all([api.operations(signal), api.nodes(signal)])
    return { operations: catalog.operations, nodes: nodes.nodes }
  })
  const [selectedID, setSelectedID] = useState('')
  const [nodeID, setNodeID] = useState('')
  const [values, setValues] = useState<FormValues>({})
  const [submitting, setSubmitting] = useState(false)
  const [submitError, setSubmitError] = useState('')
  const createOperation = useRef(new IdempotentOperation()).current

  const operations = resource.data?.operations ?? []
  const definition = useMemo(() => operations.find((item) => item.metadata.id === selectedID) ?? operations[0], [operations, selectedID])
  useEffect(() => { if (!selectedID && operations[0]) setSelectedID(operations[0].metadata.id) }, [operations, selectedID])
  useEffect(() => { setValues({}); setSubmitError(''); createOperation.complete() }, [definition?.metadata.id, createOperation])

  if (resource.loading && !resource.data) return <Loading label="正在读取受控操作目录" />
  if (resource.error && !resource.data) return <ErrorState message={resource.error} retry={resource.refresh} />
  const nodes = resource.data?.nodes ?? []
  const onlineNodes = nodes.filter((node) => node.status === 'online')
  const parameters = definition?.metadata.parameters ?? []
  const secretRequirements = definition?.metadata.secret_requirements ?? []
  const basicParameters = parameters.filter((parameter) => !operationParameterAdvanced(parameter))
  const advancedParameters = parameters.filter(operationParameterAdvanced)
  const presentation = definition ? operationPresentation(definition) : undefined
  const blockReason = definition ? operationBlockReason(definition) : ''
  const isXrocketReaddress = definition?.metadata.id === xrocketReaddressOperationID
  const requiredSecretUnavailable = Boolean(secretRequirements.some((item) => item.required) && !definition?.availability.secret_delivery)
  const unsupported = parameters.some((parameter) => !parameterSupported(parameter))
  const disabledReason = submitting ? '正在创建操作计划' : requiredSecretUnavailable ? '当前能力需要运行时秘密，但秘密交付尚未开放' : unsupported ? '当前客户端无法安全表达目录中的参数类型' : !nodeID ? '请先选择在线执行节点' : ''

  const submit = async () => {
    if (!definition || !nodeID) return
    const built = buildOperationParameters(definition, values)
    if (built.error) { setSubmitError(built.error); return }
    setSubmitting(true)
    setSubmitError('')
    const fingerprint = JSON.stringify({ operation_id: definition.metadata.id, node_id: nodeID, parameters: built.parameters })
    try {
      const run = await api.createOperationRun(definition.metadata.id, nodeID, [{ kind: 'node', node_id: nodeID }], built.parameters, [], createOperation.keyFor(fingerprint))
      createOperation.complete()
      navigate(`/operations/runs/${encodeURIComponent(run.metadata.id)}`)
    } catch (error) {
      setSubmitError(error instanceof APIError ? error.message : '创建操作计划失败')
    } finally { setSubmitting(false) }
  }

  return <>
    <PageHeader title="受控操作" description={`${operations.length} 个已注册能力；发现、前置检查、计划、受控执行、验证与安全恢复由各 Operation 能力决定`} actions={<><IconButton label="刷新" onClick={resource.refresh}><RefreshCw size={17} /></IconButton><Button onClick={() => navigate('/operations/runs')}>操作记录<ArrowRight size={16} /></Button></>} />
    {resource.error && <div className="notice-error" role="status">刷新失败：{resource.error}</div>}
    {operations.length === 0 ? <EmptyState>没有已注册的受控操作</EmptyState> : <section className="operation-layout">
      <div className="operation-catalog" aria-label="操作目录">
        {operations.map((operation) => {
          const itemPresentation = operationPresentation(operation)
          return <button key={operation.metadata.id} type="button" className={`operation-catalog-row ${operation.metadata.id === definition?.metadata.id ? 'selected' : ''}`} onClick={() => setSelectedID(operation.metadata.id)}>
            <span className="operation-icon"><Wrench size={17} /></span>
            <span><strong>{itemPresentation.name}</strong><small>{itemPresentation.category} · v{operation.metadata.version}</small><small>{operation.availability.planning ? '可生成计划' : '计划不可用'} · {operation.availability.apply ? '实际执行可用' : '实际执行未开放'}</small></span>
            <span className={`risk risk-${operation.metadata.risk}`}>{riskLabel(operation.metadata.risk)}</span>
          </button>
        })}
      </div>
      {definition && presentation && <div className="operation-composer">
        <header className="operation-definition-header"><div><h2>{presentation.name}</h2><p>{presentation.description}</p></div><details className="operation-tech-details"><summary>技术详情</summary><code>{definition.metadata.id}</code>{definition.availability.block_code && <code>{definition.availability.block_code}</code>}<small>{definition.metadata.category} · v{definition.metadata.version}</small></details></header>
        <dl className="operation-definition-facts"><div><dt>风险等级</dt><dd><span className={`risk risk-${definition.metadata.risk}`}>{riskLabel(definition.metadata.risk)}</span></dd></div><div><dt>适用系统</dt><dd>{definition.metadata.supported_systems.join(', ')}</dd></div><div><dt>影响范围</dt><dd>{presentation.impact}</dd></div></dl>
        <div className="operation-boundary"><ShieldAlert size={18} /><div><strong>{definition.availability.planning ? '计划可用' : '计划不可用'} · {definition.availability.apply ? '实际执行可用' : '实际执行未开放'}</strong><p>{blockReason || (definition.availability.apply ? '生成计划后，精确计划确认可允许 Server 继续进入受控执行、验证与必要时的安全恢复流程；仍受所有 Server 安全门约束。' : '该能力当前停止在规划与精确计划确认阶段，不会继续进入实际 Apply。')}</p></div></div>
        {isXrocketReaddress && <XrocketReaddressComposerShell />}
        <label className="field"><span>执行节点</span><select aria-label="执行节点" value={nodeID} onChange={(event) => setNodeID(event.target.value)}><option value="">请选择在线节点</option>{nodes.map((node) => <option key={node.id} value={node.id} disabled={node.status !== 'online'}>{node.hostname} · {node.os} {node.os_version}{node.status !== 'online' ? ' · 离线' : ''}</option>)}</select></label>
        {isXrocketReaddress && <div className="xrocket-target-heading"><h3>新 Master / Slave / VIP</h3><span>共享参数名保持后端契约原值</span></div>}
        <section className="operation-fields" aria-label="操作参数">{basicParameters.map((parameter) => <ParameterControl key={parameter.name} parameter={parameter} values={values} setValue={(key, value) => setValues((current) => ({ ...current, [key]: value }))} />)}</section>
        {advancedParameters.length > 0 && <details className="operation-advanced-options"><summary>高级范围选项（可选）</summary><div className="operation-fields operation-advanced-fields">{advancedParameters.map((parameter) => <ParameterControl key={parameter.name} parameter={parameter} values={values} setValue={(key, value) => setValues((current) => ({ ...current, [key]: value }))} />)}</div></details>}
        {secretRequirements.length > 0 && <section className="secret-boundary"><strong>运行时秘密边界</strong><p>{definition.availability.secret_delivery ? '仅接受不透明引用标识，页面不保存秘密内容。' : '运行时秘密交付尚未开放；页面不接收密码、Token 或私钥。'}</p><ul>{secretRequirements.map((requirement) => <li key={requirement.id}>{requirement.description || requirement.id}{requirement.required ? '（必需）' : '（可选）'}</li>)}</ul></section>}
        {requiredSecretUnavailable && <div className="notice-error">该操作需要运行时秘密，当前无法创建可执行计划。</div>}
        {unsupported && <div className="notice-error">目录包含当前客户端不支持的参数类型，已阻止提交。</div>}
        {submitError && <div className="notice-error" role="alert">{submitError}</div>}
        <div className="operation-actions"><span>{onlineNodes.length} 个在线节点 · {isXrocketReaddress ? '先完成 Discovery / Precheck，再冻结 current → target Diff' : '先生成并冻结精确操作计划'}</span><Button className="button-primary" disabled={Boolean(disabledReason)} title={disabledReason || '生成操作计划'} onClick={submit}><Play size={16} />{submitting ? '正在创建' : isXrocketReaddress ? '运行 Precheck 并生成计划' : '生成操作计划'}</Button></div>
      </div>}
    </section>}
  </>
}

function ParameterControl({ parameter, values, setValue }: { parameter: OperationParameter; values: FormValues; setValue: (key: string, value: FormValue) => void }) {
  const label = operationParameterLabel(parameter)
  if (parameter.type === 'object') return <fieldset className="operation-object"><legend>{label}{parameter.required && <span>必填</span>}</legend>{(parameter.fields ?? []).map((field) => <FieldControl key={field.name} field={field} path={`${parameter.name}.${field.name}`} value={values[`${parameter.name}.${field.name}`]} setValue={setValue} />)}</fieldset>
  return <FieldControl field={parameter} path={parameter.name} value={values[parameter.name]} setValue={setValue} />
}

function FieldControl({ field, path, value, setValue }: { field: OperationParameter | OperationParameterField; path: string; value?: FormValue; setValue: (key: string, value: FormValue) => void }) {
  const label = operationParameterLabel(field)
  if (field.type === 'boolean') return <label className="toggle-field"><input type="checkbox" checked={value === true} onChange={(event) => setValue(path, event.target.checked)} /><span>{label}</span></label>
  if (field.options?.length) return <label className="field"><span>{label}{field.required && ' *'}</span><select value={String(value ?? '')} onChange={(event) => setValue(path, event.target.value)}><option value="">请选择</option>{field.options.map((option) => <option key={option}>{option}</option>)}</select></label>
  return <label className="field"><span>{label}{field.required && ' *'}</span>{field.type === 'string[]' ? <textarea rows={3} value={String(value ?? '')} onChange={(event) => setValue(path, event.target.value)} /> : <input type={field.type === 'integer' ? 'number' : 'text'} step={field.type === 'integer' ? '1' : undefined} value={String(value ?? '')} onChange={(event) => setValue(path, event.target.value)} />}</label>
}

export function buildOperationParameters(definition: OperationDefinition, values: FormValues): { parameters: Record<string, unknown>; error?: string } {
  const parameters: Record<string, unknown> = {}
  for (const parameter of definition.metadata.parameters ?? []) {
    if (!parameterSupported(parameter)) return { parameters: {}, error: `参数 ${parameter.name} 使用了不支持的目录类型` }
    if (parameter.type === 'object') {
      const object: Record<string, unknown> = {}
      for (const field of parameter.fields ?? []) {
        const parsed = parseFieldValue(field, values[`${parameter.name}.${field.name}`])
        if (parsed.error) return { parameters: {}, error: parsed.error }
        if (parsed.missing && field.required) return { parameters: {}, error: `${operationParameterLabel(field)}为必填项` }
        if (!parsed.missing) object[field.name] = parsed.value
      }
      if (Object.keys(object).length > 0 || parameter.required) parameters[parameter.name] = object
      continue
    }
    const parsed = parseFieldValue(parameter, values[parameter.name])
    if (parsed.error) return { parameters: {}, error: parsed.error }
    if (parsed.missing && parameter.required) return { parameters: {}, error: `${operationParameterLabel(parameter)}为必填项` }
    if (!parsed.missing) parameters[parameter.name] = parsed.value
  }
  return { parameters }
}

function parseFieldValue(field: OperationParameter | OperationParameterField, value?: FormValue): { value?: unknown; missing: boolean; error?: string } {
  if (field.type === 'boolean') return value === undefined ? { missing: true } : { value: Boolean(value), missing: false }
  const text = String(value ?? '').trim()
  if (!text) return { missing: true }
  if (field.type === 'integer') {
    const parsed = Number(text)
    if (!Number.isSafeInteger(parsed)) return { missing: false, error: `${operationParameterLabel(field)}必须是整数` }
    return { value: parsed, missing: false }
  }
  if (field.type === 'string[]') return { value: text.split(/[\n,]/).map((item) => item.trim()).filter(Boolean), missing: false }
  return { value: text, missing: false }
}

function parameterSupported(parameter: OperationParameter) {
  if (sensitiveFieldName(parameter.name)) return false
  if (['string', 'integer', 'boolean', 'string[]'].includes(parameter.type)) return true
  return parameter.type === 'object' && Boolean(parameter.fields?.length) && parameter.fields!.every((field) => !sensitiveFieldName(field.name) && ['string', 'integer', 'boolean', 'string[]'].includes(field.type))
}

function sensitiveFieldName(name: string) {
  const normalized = name.toLowerCase().replace(/[^a-z0-9]/g, '')
  return ['password', 'passwd', 'secret', 'token', 'privatekey', 'authorization', 'credential', 'apikey', 'accesskey', 'secretkey', 'clientsecret', 'bearer'].some((marker) => normalized.includes(marker))
}

function riskLabel(risk: OperationDefinition['metadata']['risk']) {
  return { low: '低风险', medium: '中风险', high: '高风险', critical: '严重风险' }[risk]
}
