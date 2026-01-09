from pydantic import BaseModel


class ClientPayload(BaseModel):
    progress: float
    total: float
    description: str
    webprogress_key: str
