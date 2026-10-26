import { defineConfig } from '@lynx-js/rspeedy';
import { pluginReactLynx } from '@lynx-js/react-rsbuild-plugin';

export default defineConfig({
  plugins: [pluginReactLynx()],
  environments: { lynx: {}, web: {} },
  tools: {
    rspack: {
      module: {
        rules: [
          {
            test: /\.js$/,
            include: /node_modules[\/](marked|zod)[\/]/,
            use: {
              loader: 'babel-loader',
              options: { plugins: ['@babel/plugin-transform-unicode-property-regex'] },
            },
          },
        ],
      },
    },
  },
  source: { entry: { main: './src/index.tsx' } },
  output: { distPath: { root: 'dist' } },
});
