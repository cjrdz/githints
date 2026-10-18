import { Button } from "./components/Button";
import { Modal } from "./components/Modal";
import { formatPrice } from "./utils/format";

export default function App() {
  return <Modal><Button label={formatPrice(3)} /></Modal>;
}
