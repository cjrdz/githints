import { forwardRef } from "react";

export const Modal = forwardRef<HTMLDivElement, { children: unknown }>(function Modal(props, ref) {
  return <div ref={ref}>{props.children}</div>;
});
