import type {SidebarsConfig} from '@docusaurus/plugin-content-docs';

const sidebars: SidebarsConfig = {
  docsSidebar: [
    'intro',
    {
      type: 'category',
      label: 'Getting Started',
      items: [
        'getting-started/installation',
        'getting-started/quickstart',
      ],
    },
    {
      type: 'category',
      label: 'Concepts',
      items: [
        'concepts/bundles',
        'concepts/fragments',
        'concepts/prompts',
        'concepts/profiles',
        'concepts/remotes',
        'concepts/architecture',
      ],
    },
    {
      type: 'category',
      label: 'Guides',
      items: [
        'guides/configuration',
        'guides/mcp-server',
        'guides/templating',
        'guides/discovery',
        'guides/hooks',
        'guides/sharing',
        'guides/distillation',
        'guides/memory',
        'guides/adhoc-context',
        'guides/workflows',
      ],
    },
    {
      type: 'category',
      label: 'Reference',
      items: [
        'reference/cli',
        'reference/mcp-tools',
        'reference/environment',
      ],
    },
    'troubleshooting',
    'contributing',
  ],
};

export default sidebars;
