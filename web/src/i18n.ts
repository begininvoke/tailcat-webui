import i18n from 'i18next'
import { initReactI18next } from 'react-i18next'

export type Language = 'en' | 'zh-CN'

const resources = {
  en: {
    translation: {
      brand: { name: 'Tailcat WebUI', tagline: 'Private paths, clearly operated.' },
      nav: { overview: 'Overview', servers: 'Servers', clients: 'Clients', routes: 'Published routes', settings: 'Settings' },
      common: {
        add: 'Add', create: 'Create', cancel: 'Cancel', delete: 'Delete', start: 'Start', stop: 'Stop', close: 'Close',
        copy: 'Copy', copied: 'Copied', ping: 'Ping', open: 'Open', name: 'Name', status: 'Status', actions: 'Actions',
        loading: 'Loading', retry: 'Try again', optional: 'Optional', save: 'Save', running: 'Running', stopped: 'Stopped',
        idle: 'Idle', starting: 'Starting', connecting: 'Connecting', ready: 'Ready', stopping: 'Stopping', error: 'Error', interrupted: 'Interrupted', unavailable: 'Unavailable', more: 'More', never: 'Never', back: 'Back',
        skip: 'Skip to main content', copyFailed: 'Copy failed. Select and copy the value manually.',
      },
      auth: {
        eyebrow: 'Encrypted peer-to-peer operations', title: 'Your Tailcat network, in one calm console.',
        description: 'Run independent servers and clients, publish private resources by path, and keep every user isolated.',
        oidc: 'Continue with identity provider', demo: 'Enter demo workspace', secure: 'OIDC sessions stay in secure, HTTP-only cookies.',
        logout: 'Sign out', failed: 'Sign-in failed. Please try again.',
      },
      dashboard: {
        title: 'Network overview', subtitle: 'A current view of your Tailcat runtimes and published paths.',
        servers: 'Servers', clients: 'Clients', routes: 'Published routes', running: '{{count}} running',
        reachable: '{{count}} checked', public: '{{count}} public', recentServers: 'Recent servers', recentClients: 'Recent clients',
        quickStart: 'Quick start', addServer: 'Create a server', addClient: 'Connect a client', publishRoute: 'Publish a route',
        empty: 'Your workspace is ready. Create a server to receive your first Tailcat token.',
      },
      servers: {
        title: 'Tailcat servers', subtitle: 'Each server owns an independent WireGuard identity, netstack and DERP connection.',
        new: 'New server', empty: 'No servers yet', emptyDescription: 'Create an ephemeral server for a one-off path or save its identity for a stable token.',
        keyMode: 'Identity', ephemeral: 'Ephemeral', saved: 'Saved', region: 'DERP region', auto: 'Automatic', customMap: 'Custom DERP map URL',
        exitNode: 'Enable exit-node forwarding', startNow: 'Start after creation', token: 'Connection token', publicKey: 'Public key',
        mappings: 'Port mappings', mappingsHint: 'Stop the server before adding mappings; removing a live mapping safely stops it.', addMapping: 'Add mapping',
        allowlist: 'Allowed client keys', allowlistHint: 'Once the first key is added, unknown clients are silently ignored. Revocation safely stops a running server.', addAllowed: 'Allow client',
        allowAll: 'Open to token holders', denyUnknown: 'Unknown clients denied',
        listenPort: 'Tailcat port', target: 'Local target', kind: 'Mode', tcp: 'TCP forward', ssh: 'Auth-free SSH',
        targetHost: 'Target host', targetPort: 'Target port', deleteTitle: 'Delete this server?', deleteDescription: 'Its saved identity and mappings will be removed.',
        startFailed: 'The server could not start.', createFailed: 'The server could not be created.',
      },
      clients: {
        title: 'Tailcat clients', subtitle: 'Keep several remote identities and test whether paths are direct or relayed.',
        new: 'New client', empty: 'No clients yet', emptyDescription: 'Paste a Tailcat token or a DNS name with a tailcat= TXT record.',
        server: 'Connection token or DNS name', saveIdentity: 'Save client identity', saveIdentityHint: 'Required when the remote server allowlists this client.',
        tokenHint: 'Token', lastPing: 'Last ping', path: 'Path', direct: 'Direct', derp: 'DERP relay', peerRelay: 'Peer relay',
        deleteTitle: 'Delete this client?', deleteDescription: 'Published routes using it must be removed first.', pingFailed: 'The remote server did not respond.',
        tools: 'Token tools', parse: 'Parse token', resolve: 'Resolve token', tokenInput: 'Tailcat token', result: 'Result',
        tunnel: 'TCP tunnel', tunnelAddress: 'Remote address', connect: 'Connect', disconnect: 'Disconnect', send: 'Send', input: 'Input', output: 'Output', connected: 'Tunnel connected.',
      },
      routes: {
        title: 'Published routes', subtitle: 'Expose a remote HTTP, SSE or WebSocket service below a stable local path.',
        new: 'Publish route', empty: 'No published routes', emptyDescription: 'Connect a client first, then map one remote port to a memorable path.',
        client: 'Tailcat client', slug: 'Path slug', remotePort: 'Remote port', basePath: 'Remote base path', access: 'Access',
        private: 'Signed-in owner', public: 'Public link', preview: 'Published URL', deleteTitle: 'Remove this route?',
        methods: 'Allowed HTTP methods', methodsHint: 'GET and HEAD are safe defaults. Enable mutating methods only when the upstream API requires them.',
        deleteDescription: 'Requests to its path will stop immediately.', noClients: 'Create a client before publishing a route.',
      },
      settings: {
        title: 'Settings', subtitle: 'Appearance, language and deployment details.', appearance: 'Appearance', language: 'Language',
        light: 'Light', dark: 'Dark', system: 'System', english: 'English', chinese: '简体中文', account: 'Account',
        deployment: 'Deployment', version: 'Version', authMode: 'Authentication', oidc: 'OpenID Connect', demo: 'Local demo mode',
        themeHint: 'System follows your device and updates without reloading.', languageHint: 'Dates and Ant Design component copy switch together.',
      },
      feedback: { loadFailed: 'Could not load this page.', createFailed: 'Could not create the resource.', deleteFailed: 'Could not delete the resource.', deleted: 'Resource deleted.' },
      validation: { required: 'This field is required.', slug: 'Use 3–63 lowercase letters, numbers or hyphens.', port: 'Enter a port from 1 to 65535.' },
    },
  },
  'zh-CN': {
    translation: {
      brand: { name: 'Tailcat WebUI', tagline: '私密路径，清晰管理。' },
      nav: { overview: '概览', servers: '服务端', clients: '客户端', routes: '发布路由', settings: '设置' },
      common: {
        add: '添加', create: '创建', cancel: '取消', delete: '删除', start: '启动', stop: '停止', close: '关闭',
        copy: '复制', copied: '已复制', ping: '测试连接', open: '打开', name: '名称', status: '状态', actions: '操作',
        loading: '正在加载', retry: '重试', optional: '可选', save: '保存', running: '运行中', stopped: '已停止',
        idle: '未连接', starting: '正在启动', connecting: '正在连接', ready: '就绪', stopping: '正在停止', error: '错误', interrupted: '已中断', unavailable: '不可用', more: '更多', never: '从未', back: '返回',
        skip: '跳到主要内容', copyFailed: '复制失败，请手动选择并复制。',
      },
      auth: {
        eyebrow: '端到端加密的点对点运维', title: '一个清爽的控制台，管理你的 Tailcat 网络。',
        description: '运行相互独立的服务端和客户端，按路径发布私有资源，并隔离每一位用户的数据。',
        oidc: '使用身份提供商登录', demo: '进入演示工作区', secure: 'OIDC 会话仅保存在安全的 HttpOnly Cookie 中。',
        logout: '退出登录', failed: '登录失败，请重试。',
      },
      dashboard: {
        title: '网络概览', subtitle: '查看 Tailcat 运行实例和已发布路径的当前状态。',
        servers: '服务端', clients: '客户端', routes: '发布路由', running: '{{count}} 个运行中',
        reachable: '{{count}} 个已检测', public: '{{count}} 个公开', recentServers: '最近服务端', recentClients: '最近客户端',
        quickStart: '快速开始', addServer: '创建服务端', addClient: '连接客户端', publishRoute: '发布路由',
        empty: '工作区已就绪。创建服务端即可获得第一个 Tailcat 连接令牌。',
      },
      servers: {
        title: 'Tailcat 服务端', subtitle: '每个服务端都有独立的 WireGuard 身份、netstack 和 DERP 连接。',
        new: '新建服务端', empty: '还没有服务端', emptyDescription: '临时身份适合一次性连接；保存身份可获得跨重启稳定的令牌。',
        keyMode: '身份类型', ephemeral: '临时', saved: '保存', region: 'DERP 区域', auto: '自动选择', customMap: '自定义 DERP Map 地址',
        exitNode: '启用出口节点转发', startNow: '创建后立即启动', token: '连接令牌', publicKey: '公钥',
        mappings: '端口映射', mappingsHint: '添加映射前请先停止服务端；删除在线映射会安全停止服务端。', addMapping: '添加映射',
        allowlist: '允许的客户端公钥', allowlistHint: '添加第一个公钥后，未知客户端会被静默忽略；撤销公钥会安全停止运行中的服务端。', addAllowed: '允许客户端',
        allowAll: '持有令牌即可连接', denyUnknown: '拒绝未知客户端',
        listenPort: 'Tailcat 端口', target: '本地目标', kind: '模式', tcp: 'TCP 转发', ssh: '免认证 SSH',
        targetHost: '目标主机', targetPort: '目标端口', deleteTitle: '删除这个服务端？', deleteDescription: '保存的身份和端口映射也会被删除。',
        startFailed: '服务端启动失败。', createFailed: '服务端创建失败。',
      },
      clients: {
        title: 'Tailcat 客户端', subtitle: '保存多个远端身份，并检测当前连接是直连还是中继。',
        new: '新建客户端', empty: '还没有客户端', emptyDescription: '粘贴 Tailcat 令牌，或填写带有 tailcat= TXT 记录的域名。',
        server: '连接令牌或 DNS 名称', saveIdentity: '保存客户端身份', saveIdentityHint: '远端服务端启用客户端白名单时必须保存身份。',
        tokenHint: '令牌', lastPing: '最近检测', path: '路径', direct: '直连', derp: 'DERP 中继', peerRelay: 'Peer Relay',
        deleteTitle: '删除这个客户端？', deleteDescription: '需要先删除使用它的发布路由。', pingFailed: '远端服务端没有响应。',
        tools: '令牌工具', parse: '解析令牌', resolve: '解析为完整令牌', tokenInput: 'Tailcat 令牌', result: '结果',
        tunnel: 'TCP 隧道', tunnelAddress: '远端地址', connect: '连接', disconnect: '断开', send: '发送', input: '输入', output: '输出', connected: '隧道已连接。',
      },
      routes: {
        title: '发布路由', subtitle: '将远端 HTTP、SSE 或 WebSocket 服务发布到稳定的本地子路径。',
        new: '发布路由', empty: '还没有发布路由', emptyDescription: '先连接一个客户端，再把远端端口映射为容易记忆的路径。',
        client: 'Tailcat 客户端', slug: '路径名称', remotePort: '远端端口', basePath: '远端基础路径', access: '访问权限',
        private: '仅当前登录用户', public: '公开链接', preview: '发布地址', deleteTitle: '移除这个路由？',
        methods: '允许的 HTTP 方法', methodsHint: 'GET 和 HEAD 是安全默认值；仅在上游 API 确有需要时开放写入方法。',
        deleteDescription: '对应路径会立即停止服务。', noClients: '请先创建客户端，再发布路由。',
      },
      settings: {
        title: '设置', subtitle: '外观、语言和部署信息。', appearance: '外观', language: '语言',
        light: '浅色', dark: '深色', system: '跟随系统', english: 'English', chinese: '简体中文', account: '账户',
        deployment: '部署信息', version: '版本', authMode: '身份认证', oidc: 'OpenID Connect', demo: '本地演示模式',
        themeHint: '跟随系统会随设备设置自动变化，无需刷新页面。', languageHint: '日期和 Ant Design 组件文案会同步切换。',
      },
      feedback: { loadFailed: '页面加载失败。', createFailed: '资源创建失败。', deleteFailed: '资源删除失败。', deleted: '资源已删除。' },
      validation: { required: '此项必填。', slug: '请输入 3–63 位小写字母、数字或连字符。', port: '请输入 1 到 65535 之间的端口。' },
    },
  },
} as const

i18n.use(initReactI18next).init({
  resources,
  lng: 'en',
  fallbackLng: 'en',
  interpolation: { escapeValue: false },
})

export default i18n
