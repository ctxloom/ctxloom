// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

export default defineConfig({
	site: 'https://ctxloom.dev',
	integrations: [
		starlight({
			title: 'ctxloom',
			description: 'Context Loom - Weave context for AI coding agents',
			favicon: '/favicon.ico',
			social: {
				github: 'https://github.com/ctxloom/ctxloom',
			},
			editLink: {
				baseUrl: 'https://github.com/ctxloom/ctxloom/edit/main/website/',
			},
			customCss: ['./src/styles/custom.css'],
			lastUpdated: true,
			sidebar: [
				{
					label: 'Introduction',
					link: '/',
				},
				{
					label: 'Getting Started',
					items: [
						{ label: 'Installation', link: '/getting-started/installation/' },
						{ label: 'Quick Start', link: '/getting-started/quickstart/' },
						{ label: 'Authoring Bundles', link: '/getting-started/authoring/' },
						{ label: 'Session Memory', link: '/getting-started/memory/' },
					],
				},
				{
					label: 'Concepts',
					items: [
						{ label: 'Bundles', link: '/concepts/bundles/' },
						{ label: 'Fragments', link: '/concepts/fragments/' },
						{ label: 'Prompts', link: '/concepts/prompts/' },
						{ label: 'Profiles', link: '/concepts/profiles/' },
						{ label: 'Remotes', link: '/concepts/remotes/' },
						{ label: 'Architecture', link: '/concepts/architecture/' },
					],
				},
				{
					label: 'Guides',
					items: [
						{ label: 'Configuration', link: '/guides/configuration/' },
						{ label: 'MCP Server', link: '/guides/mcp-server/' },
						{ label: 'Templating', link: '/guides/templating/' },
						{ label: 'Discovery', link: '/guides/discovery/' },
						{ label: 'Hooks', link: '/guides/hooks/' },
						{ label: 'Sharing', link: '/guides/sharing/' },
						{ label: 'Distillation', link: '/guides/distillation/' },
						{ label: 'Ad-hoc Context', link: '/guides/adhoc-context/' },
						{ label: 'Workflows', link: '/guides/workflows/' },
					],
				},
				{
					label: 'Reference',
					items: [
						{ label: 'CLI', link: '/reference/cli/' },
						{ label: 'MCP Tools', link: '/reference/mcp-tools/' },
						{ label: 'Environment', link: '/reference/environment/' },
					],
				},
				{ label: 'Troubleshooting', link: '/troubleshooting/' },
				{ label: 'Contributing', link: '/contributing/' },
			],
		}),
	],
});
