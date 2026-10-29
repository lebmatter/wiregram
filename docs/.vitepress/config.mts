import { defineConfig } from 'vitepress'

const repo = 'https://github.com/lebmatter/wiregram'

export default defineConfig({
  title: 'Wiregram',
  description: 'A multi-backend chat client for the terminal',
  cleanUrls: true,
  lastUpdated: true,
  head: [['link', { rel: 'icon', type: 'image/svg+xml', href: '/favicon.svg' }]],

  themeConfig: {
    logo: '/mark.svg',
    search: { provider: 'local' },

    nav: [
      { text: 'Docs', link: '/' },
      { text: 'Website', link: 'https://wiregram.chat' },
      { text: 'Releases', link: `${repo}/releases` },
    ],

    sidebar: [
      {
        text: 'Get Started',
        items: [
          { text: 'Welcome', link: '/' },
          { text: 'Installation', link: '/installation' },
          { text: 'Usage', link: '/usage' },
          { text: 'Keybinds', link: '/keybinds' },
          { text: 'Configuration', link: '/configuration' },
        ],
      },
      {
        text: 'Setup Guides',
        items: [
          { text: 'Telegram', link: '/setup-guides/telegram' },
          { text: 'WhatsApp', link: '/setup-guides/whatsapp' },
          { text: 'Matrix', link: '/setup-guides/matrix' },
          { text: 'Mattermost', link: '/setup-guides/mattermost' },
          { text: 'Email', link: '/setup-guides/email' },
        ],
      },
      {
        text: 'Features',
        items: [
          { text: 'Accounts', link: '/features/accounts' },
          { text: 'Chats', link: '/features/chats' },
          { text: 'Unread & Mentions', link: '/features/unread-mentions' },
          { text: 'Media', link: '/features/media' },
          { text: 'Email', link: '/features/email' },
          { text: 'Themes', link: '/features/themes' },
          { text: 'AI Powered Chat Bots', link: '/features/chat-bots' },
          { text: 'Summaries & Rewrites', link: '/features/ai' },
          { text: 'Plugins', link: '/features/plugins' },
          { text: 'Games', link: '/features/games' },
          { text: 'CLI', link: '/features/cli' },
        ],
      },
      {
        text: 'For Developers',
        items: [{ text: 'Building', link: '/for-developers/building' }],
      },
      {
        text: 'Community',
        items: [
          { text: 'GitHub', link: repo },
          { text: 'Issues', link: `${repo}/issues` },
          { text: 'wiregram.chat', link: 'https://wiregram.chat' },
        ],
      },
    ],

    socialLinks: [{ icon: 'github', link: repo }],

    editLink: {
      pattern: `${repo}/edit/main/docs/:path`,
      text: 'Edit this page on GitHub',
    },

    footer: {
      message: 'Released under the AGPL v3 License.',
      copyright: 'Copyright © 2026 lebmatter',
    },
  },
})
