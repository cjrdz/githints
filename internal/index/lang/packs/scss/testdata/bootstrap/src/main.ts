import "./scss/custom.scss";
import "bootstrap/dist/js/bootstrap.bundle.min.js";
import { Tooltip } from "bootstrap";

export const tip = (el: Element) => new Tooltip(el);
