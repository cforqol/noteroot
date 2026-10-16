export default {
  testEnvironment: 'jsdom',
  testMatch: ['**/__tests__/**/*.test.js'],
  transform: {
    '^.+\\.js$': ['babel-jest', { presets: [['@babel/preset-env', { targets: { node: 'current' } }]] }],
  },
  transformIgnorePatterns: ['/node_modules/'],
  collectCoverageFrom: [
    'lib/**/*.js',
    '!**/node_modules/**',
    '!**/vim/**',
  ],
  coverageThreshold: {
    global: {
      branches: 95,
      functions: 100,
      lines: 100,
      statements: 100,
    },
  },
  setupFilesAfterEnv: ['<rootDir>/__tests__/setup.js'],
  moduleNameMapper: {
    '^/vim/vimwasm\\.js$': '<rootDir>/__tests__/mocks/vimwasm.js',
    '^/lib/vim-mode\\.js$': '<rootDir>/lib/vim-mode.js',
    '^/lib/panels\\.js$': '<rootDir>/lib/panels.js',
  },
};
