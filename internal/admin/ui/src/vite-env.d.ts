/// <reference types="vite/client" />

declare module "*.css" {
  const classes: Readonly<Record<string, string>>;
  export default classes;
}
