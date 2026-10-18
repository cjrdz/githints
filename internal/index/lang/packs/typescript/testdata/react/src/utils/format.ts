// A capitalised helper in a .ts file is not a component.
export function Currency(code: string) {
  return code.toUpperCase();
}

export function formatPrice(n: number) {
  return `$${n}`;
}
