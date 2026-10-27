import React, { useEffect, useState } from 'react'
import { createRoot } from 'react-dom/client'
import './style.css'
import { request } from './api.js'

const MCP_URL = 'https://mail.microapps.space/mcp'

function App() {
  const [account, setAccount] = useState(null)
  const [icloudEmail, setIcloudEmail] = useState('')
  const [icloudPassword, setIcloudPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState(() => new URLSearchParams(window.location.search).get('verified') === '1' ? 'Email verified. Your account is ready.' : '')
  const [copied, setCopied] = useState(false)

  async function loadAccount() {
    try {
      setAccount(await request('/api/me'))
    } catch (err) {
      if (err.status === 401) { setAccount(false); setError('') }
      else setError(err.message)
    }
  }
  useEffect(() => { loadAccount() }, [])

  async function connect(event) {
    event.preventDefault()
    setBusy(true); setError(''); setNotice('')
    try {
      await request('/api/account', { method: 'PUT', body: JSON.stringify({ email: icloudEmail, password: icloudPassword }) })
      setIcloudPassword(''); setNotice('Your iCloud Mail account is connected.')
      await loadAccount()
    } catch (err) { setError(err.message) }
    finally { setBusy(false) }
  }

  async function disconnect() {
    if (!window.confirm('Disconnect this iCloud Mail account?')) return
    setBusy(true); setError(''); setNotice('')
    try {
      await request('/api/account', { method: 'DELETE' })
      setAccount({ ...account, connected: false, icloud_email: '' })
      setNotice('Your iCloud Mail account has been disconnected.')
    } catch (err) { setError(err.message) }
    finally { setBusy(false) }
  }

  async function logout() {
    setBusy(true)
    try { await request('/auth/logout', { method: 'POST' }); window.location.assign('/') }
    catch (err) { setError(err.message); setBusy(false) }
  }

  async function copyMcp() {
    try { await navigator.clipboard.writeText(MCP_URL); setCopied(true); setTimeout(() => setCopied(false), 1800) }
    catch { setError('Could not copy the URL. Select and copy it instead.') }
  }

  if (account === null) return error ? <main className="loading-error"><section className="signin-card"><h1>We couldn’t load your account</h1><p className="alert" role="alert">{error}</p><button className="button primary" onClick={() => { setError(''); loadAccount() }}>Try again</button></section></main> : <main className="loading" aria-live="polite">Loading your account…</main>
  if (account === false) return <AuthScreen error={error} clearError={() => setError('')} />

  return <div className="app-shell">
    <header className="topbar">
      <a className="brand" href="/" aria-label="Mail MCP home"><span className="brand-icon">✉</span><span>mail<span className="brand-muted">.mcp</span></span></a>
      <div className="user-menu"><span className="avatar" aria-hidden="true">{(account.email || '?').slice(0, 1).toUpperCase()}</span><span className="user-email">{account.email}</span><button className="text-button" onClick={logout} disabled={busy}>Sign out</button></div>
    </header>
    <main className="dashboard">
      <div className="page-heading"><div><span className="eyebrow">YOUR WORKSPACE</span><h1>Mail connections</h1><p>Connect iCloud Mail, then choose it from any MCP client.</p></div><span className="secure-tag"><span className="secure-dot" /> PRIVATE WORKSPACE</span></div>
      {error && <p className="alert" role="alert">{error}</p>}{notice && <p className="notice" role="status">{notice}</p>}
      <section className="panel connection-panel" aria-labelledby="icloud-heading">
        <div className="panel-top"><div className="provider"><span className="apple-icon" aria-hidden="true">☁</span><div><span className="eyebrow">EMAIL PROVIDER</span><h2 id="icloud-heading">iCloud Mail</h2></div></div>
          {account.connected ? <span className="status connected"><span /> Connected</span> : <span className="status"><span /> Not connected</span>}
        </div>
        {account.connected ? <div className="connected-body">
          <div className="mailbox"><span className="mailbox-icon">✉</span><div><span className="field-label">CONNECTED MAILBOX</span><strong>{account.icloud_email}</strong></div></div>
          <button className="button danger" onClick={disconnect} disabled={busy}>{busy ? 'Please wait…' : 'Disconnect account'}</button>
        </div> : <div className="form-body">
          <p className="form-intro">Use an app-specific password to securely connect your mailbox. Your regular Apple ID password will not work.</p>
          <form onSubmit={connect}>
            <label htmlFor="icloud-email">iCloud email address</label>
            <input id="icloud-email" type="email" autoComplete="email" placeholder="you@icloud.com" required value={icloudEmail} onChange={e => setIcloudEmail(e.target.value)} />
            <label htmlFor="icloud-password">App-specific password</label>
            <input id="icloud-password" type="password" autoComplete="new-password" placeholder="xxxx-xxxx-xxxx-xxxx" required value={icloudPassword} onChange={e => setIcloudPassword(e.target.value)} />
            <div className="form-actions"><button className="button primary" type="submit" disabled={busy}>{busy ? 'Connecting…' : 'Connect iCloud Mail'} <span aria-hidden="true">→</span></button><a href="https://account.apple.com/" target="_blank" rel="noreferrer">How to create an app password ↗</a></div>
          </form>
          <div className="hint"><span aria-hidden="true">ⓘ</span><p>Generate a password at <strong>account.apple.com → Sign-In and Security → App-Specific Passwords</strong>. You can revoke it anytime from your Apple account.</p></div>
        </div>}
      </section>
      <section className="panel mcp-panel" aria-labelledby="mcp-heading">
        <div className="mcp-header"><div><span className="eyebrow">USE YOUR MAIL IN AI CLIENTS</span><h2 id="mcp-heading">Connect with MCP</h2><p>Add this server to ChatGPT, Grok, or another MCP client.</p></div><span className="mcp-mark">MCP</span></div>
        <label className="field-label" htmlFor="mcp-url">SERVER URL</label>
        <div className="url-control"><code id="mcp-url">{MCP_URL}</code><button type="button" onClick={copyMcp} className="copy-button" aria-label="Copy MCP server URL">{copied ? 'Copied ✓' : 'Copy URL'}</button></div>
        <details><summary>Client setup</summary><p>In your MCP client, add a remote server using the URL above. Sign in with your mail.mcp account when prompted. Your connected iCloud mailbox will then be available to the client.</p></details>
      </section>
      <p className="dashboard-foot">Your iCloud credentials are used only to access your mailbox. <span>·</span> <a href="mailto:support@microapps.space">Need help?</a></p>
    </main>
  </div>
}

function AuthScreen({ error, clearError }) {
  const params = new URLSearchParams(window.location.search)
  const resetToken = params.get('reset_token') || ''
  const next = params.get('next') || ''
  const [mode, setMode] = useState(resetToken ? 'reset' : 'login')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [authError, setAuthError] = useState('')
  const [notice, setNotice] = useState(
    params.get('verified') === '1' ? 'Email verified. You can now sign in.' :
    params.get('error') === 'verification' ? 'That verification link is invalid or expired. Request a new one by registering again.' :
    params.get('error') === 'waitlist' ? 'Access is limited to @olegpustovit.com addresses and ursus95@gmail.com.' :
    params.get('login') === '1' ? 'Sign in to continue connecting your MCP client.' : '',
  )

  async function submit(event) {
    event.preventDefault()
    setBusy(true); setAuthError(''); setNotice(''); clearError()
    try {
      if (mode === 'login') {
        const result = await request('/auth/login', { method: 'POST', body: JSON.stringify({ email, password, next: next || undefined }) })
        const destination = result?.next || next
        window.location.assign(destination && destination.startsWith('/') && !destination.startsWith('//') ? destination : '/')
      } else if (mode === 'register') {
        const result = await request('/auth/register', { method: 'POST', body: JSON.stringify({ email, password, next: next || undefined }) })
        setNotice(result?.message || 'Check your email to verify your account.')
        setMode('login'); setPassword('')
      } else if (mode === 'forgot') {
        const result = await request('/auth/forgot', { method: 'POST', body: JSON.stringify({ email }) })
        setNotice(result?.message || 'If that address is eligible, a reset link will be sent.')
        setMode('login')
      } else {
        await request('/auth/reset', { method: 'POST', body: JSON.stringify({ token: resetToken, password }) })
        setPassword(''); setNotice('Password updated. Sign in with your new password.')
        setMode('login')
      }
    } catch (err) {
      const messages = {
        invalid_credentials: 'Email or password is incorrect.',
        email_not_allowed: 'Access is limited to @olegpustovit.com addresses and ursus95@gmail.com.',
        password_must_be_12_to_128_characters: 'Choose a password between 12 and 128 characters.',
        invalid_or_expired_reset_token: 'This link is invalid or expired. Request a fresh link and try again.',
      }
      setAuthError(messages[err.message] || err.message)
    } finally { setBusy(false) }
  }

  const titles = { login: 'Welcome back', register: 'Create your account', forgot: 'Reset your password', reset: 'Choose a new password' }
  const descriptions = {
    login: 'Sign in to connect your iCloud Mail and MCP clients.',
    register: 'Create an account with an invited email address.',
    forgot: 'We’ll email a password reset link if this account is eligible.',
    reset: 'Choose a new password for your account.',
  }
  return <main className="signin">
    <a className="brand" href="/" aria-label="Mail MCP home"><span className="brand-icon">✉</span><span>mail<span className="brand-muted">.mcp</span></span></a>
    <section className="signin-card">
      <span className="eyebrow">PRIVATE MAIL, READY FOR AI</span>
      <h1>{titles[mode]}</h1>
      <p>{descriptions[mode]}</p>
      {notice && <p className="notice" role="status">{notice}</p>}
      {(authError || error) && <p className="alert" role="alert">{authError || error}</p>}
      <form className="auth-form" onSubmit={submit}>
        {mode !== 'reset' && <><label htmlFor="auth-email">Email address</label><input id="auth-email" type="email" autoComplete="email" required value={email} onChange={e => setEmail(e.target.value)} placeholder="you@example.com" />
          {mode === 'register' && <p className="auth-hint">Allowed: @olegpustovit.com and ursus95@gmail.com</p>}</>}
        {mode !== 'forgot' && <><label htmlFor="auth-password">{mode === 'reset' ? 'New password' : 'Password'}</label><input id="auth-password" type="password" autoComplete={mode === 'login' ? 'current-password' : 'new-password'} minLength={mode === 'login' ? undefined : 12} maxLength={128} required value={password} onChange={e => setPassword(e.target.value)} placeholder={mode === 'login' ? 'Your password' : 'At least 12 characters'} /></>}
        <button className="button primary auth-submit" type="submit" disabled={busy}>{busy ? 'Please wait…' : { login: 'Sign in', register: 'Create account', forgot: 'Send reset link', reset: 'Update password' }[mode]}</button>
      </form>
      <div className="auth-links">
        {mode === 'login' && <><button type="button" className="link-button" onClick={() => { setMode('forgot'); setAuthError('') }}>Forgot password?</button><span>New here? <button type="button" className="link-button" onClick={() => { setMode('register'); setAuthError('') }}>Create account</button></span></>}
        {(mode === 'register' || mode === 'forgot' || mode === 'reset') && <button type="button" className="link-button" onClick={() => { setMode('login'); setAuthError('') }}>Back to sign in</button>}
      </div>
      <p className="privacy-note">Only invited email addresses can register.</p>
    </section>
    <footer>Private by design <span>·</span> Your credentials stay encrypted</footer>
  </main>
}


createRoot(document.getElementById('root')).render(<React.StrictMode><App /></React.StrictMode>)
